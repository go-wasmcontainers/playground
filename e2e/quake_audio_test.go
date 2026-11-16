//go:build e2e

package e2e

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// The paced mixer's real-time ratio must lie in [pacedRatioMin, pacedRatioMax].
// The ratio is mix frames offered per WALL second over 11025. A tic longer than
// the mix-ahead (0.1 s) is not paid back (a stall is not a debt), so a loaded
// runner offers less than real time: the measured CI spread is 0.746 (one red
// run of playground #66), 0.884 (#63's lane), 1.01 (an idle machine). That is
// the game falling behind the clock, not the mixer under-producing: the mixer
// offers what the game's tics owe. 0.6 leaves room under the worst reading and
// still fails a starved mixer (half rate). Whether the SOUND underran is a
// different question, asserted on the page's stream, not here. The upper bound
// catches the old defect: the fixed 512-frame chunk is 2.7 to 2.8 at 60 frames
// a second, far above 1.1 (on a runner as slow as 15 frames a second the fixed
// chunk itself reads 0.86 and only the log's predicted-ratio check sees it).
const pacedRatioMin = 0.6

func pacedRatioMax(seconds float64) float64 { return 1 + 0.06/seconds + 0.08 }

func pacedRatioOK(ratio, seconds float64) bool {
	return ratio >= pacedRatioMin && ratio <= pacedRatioMax(seconds)
}

// signalFloor is the worklet's RMS (linear, -60 dBFS) that a window of Quake's
// sound must exceed.
const signalFloor = 1e-3

// requireSignal samples the worklet's RMS every 150 ms for seconds and fails
// unless EVERY window of 5 s holds a reading above signalFloor. Quake's
// ambient sounds loop (go-quake1/engine v0.26.0, snd_mix.c S_PaintChannels:
// at the end of a looping channel pos = loopstart); before that they played
// once and the mixer went to exact silence at about 25 s of mix. A test that
// asked for sound only in its first seconds could not see it.
func requireSignal(t *testing.T, what string, seconds int, rms func() float64) {
	t.Helper()
	const window = 5 * time.Second
	start := time.Now()
	var worst float64 = -1
	for w := 0; time.Since(start) < time.Duration(seconds)*time.Second; w++ {
		var peak float64
		end := time.Now().Add(window)
		for time.Now().Before(end) {
			peak = max(peak, rms())
			time.Sleep(150 * time.Millisecond)
		}
		t.Logf("%s: %3.0f s: the worklet's peak RMS in the last 5 s is %.4f", what, time.Since(start).Seconds(), peak)
		if worst < 0 || peak < worst {
			worst = peak
		}
		if peak < signalFloor {
			t.Fatalf("%s: no sound in the 5 s window ending at %.0f s (peak RMS %.5f, floor %g): the mixer went silent", what, time.Since(start).Seconds(), peak, signalFloor)
		}
	}
}

// A pacing regression to the fixed chunk (2.75 measured at 60 frames a second,
// 2.2 at 48, 1.3 at 28) fails; the loaded runner (0.884) and the idle machine
// (1.01) pass.
func TestPacedRatioBounds(t *testing.T) {
	for _, c := range []struct {
		ratio float64
		ok    bool
	}{{2.75, false}, {2.2, false}, {1.3, false}, {1.01, true}, {0.884, true}, {0.746, true}, {0.55, false}, {0, false}} {
		if got := pacedRatioOK(c.ratio, 5); got != c.ok {
			t.Errorf("ratio %.3f over 5 s: accepted=%v, want %v", c.ratio, got, c.ok)
		}
	}
}

// TestQuakeAudio plays Quake with sound (go-quake1/engine#107): the quake image
// run with --audio writes its mix through wctr_audio_out, the page plays it, and
//
//   - the mixer is paced: over a 300-frame run the mix frames offered per second
//     of game time over the 11025 a second the output plays (the real-time
//     ratio the guest prints) is near 1.00 (the lead it prefills on top), where
//     the fixed 512-frame chunk the game painted before the pacing is
//     512 x frames per second / 11025 (--fixed-mixer reproduces it, for the
//     before/after in this log);
//   - without --audio the host answers denied, the game runs silent and raises
//     nothing;
//   - detached with --audio, once the page has played (a click first), the
//     stream's played counter moves, and the worklet's heartbeat with it.
//
// It needs a real GPU as TestQuake does (E2E_GPU=1: the engine refuses software
// adapters, so there is no frame to play against).
func TestQuakeAudio(t *testing.T) {
	if os.Getenv("E2E_GPU") != "1" || os.Getenv("E2E_URL") != "" {
		t.Skip("set E2E_GPU=1 on a lane with WebGPU")
	}
	pw, err := playwright.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Stop()
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true), Channel: playwright.String("chromium"),
		IgnoreDefaultArgs: []string{"--autoplay-policy=no-user-gesture-required"},
		Args: append([]string{"--enable-unsafe-webgpu", "--autoplay-policy=document-user-activation-required"},
			strings.Fields(os.Getenv("E2E_GPU_ARGS"))...),
		Timeout: playwright.Float(180000)})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer browser.Close()
	a := freshTab(t, browser, serve(t, true), "chromium/quake-audio")

	info, _ := a.eval(`async () => {
	  if (!navigator.gpu) return "no navigator.gpu";
	  const ad = await navigator.gpu.requestAdapter();
	  if (!ad) return "no adapter";
	  const i = ad.info || {};
	  return [i.vendor, i.architecture, i.device, i.description, (i.isFallbackAdapter || ad.isFallbackAdapter) ? "fallback" : ""].join("|");
	}`).(string)
	t.Logf("adapter: %s", info)
	if strings.Contains(strings.ToLower(info), "swiftshader") || strings.Contains(info, "fallback") || strings.HasPrefix(info, "no ") {
		if os.Getenv("E2E_GPU_REAL") == "1" {
			t.Fatalf("a software adapter on the GPU lane: %s", info)
		}
		t.Skipf("no real GPU adapter (%s)", info)
	}
	t.Logf("1-minute load %s (above 8 the frame rate, not the ratio, is not a measurement)", loadAvg())

	// A click first: the context then starts as soon as the first stream
	// opens (sticky activation), so the runs below play instead of waiting.
	a.click()

	ratio := func(out string) (float64, int, float64) {
		t.Helper()
		m := regexp.MustCompile(`audio: (playing|silent), (\d+) mix frames offered, (\d+) accepted, in ([0-9.]+) s: real-time ratio ([0-9.]+)`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("no audio line in:\n%s", out)
		}
		r, _ := strconv.ParseFloat(m[5], 64)
		n, _ := strconv.Atoi(m[2])
		s, _ := strconv.ParseFloat(m[4], 64)
		return r, n, s
	}
	const run = "wctr run --rm --gpu --audio -m 512m quake --map e1m1 --frames 300"

	// 0. The pak the image carries gives the ambient water its loop point (freepak
	// v0.2.1; LibreQuake's water1.wav had none, so it played once and the level
	// went quiet): the game logs what it loads. e1m1 has water, and its ambient
	// is on a reserved static channel from the start.
	wlog := a.must("wctr run --rm --gpu --audio -m 512m quake --map e1m1 --frames 1", 0)
	if m := regexp.MustCompile(`loaded WAV sound/ambience/water1\.wav -- rate=11025Hz bits=8 numSamples=(\d+) loopStart=(-?\d+)`).FindStringSubmatch(wlog); m == nil {
		t.Errorf("the game did not log loading ambience/water1.wav:\n%s", wlog)
	} else if m[2] != "0" {
		t.Errorf("water1.wav has loopStart=%s (of %s samples), want its cue point 0: the pak lacks freepak v0.2.1's loop point", m[2], m[1])
	}

	// 1. Before: the fixed chunk (what the game did before the pacing).
	out := a.must(run+" --fixed-mixer", 0, "sound on (wctr_audio_out, 11025 Hz stereo)", "audio: playing")
	before, n0, s0 := ratio(out)
	// 2. After: paced.
	out = a.must(run, 0, "sound on (wctr_audio_out, 11025 Hz stereo)", "audio: playing")
	after, n1, s1 := ratio(out)
	t.Logf("real-time ratio in Chromium: fixed chunk %.3f (%d mix frames in %.2f s), paced %.3f (%d in %.2f s)", before, n0, s0, after, n1, s1)
	// The fixed chunk is 512 frames a tic: its ratio is the frame rate over
	// 21.5 (11025 / 512), which this run's own frame rate predicts.
	if want := 512 * 300 / (s0 * 11025); before < want-0.05 || before > want+0.05 {
		t.Errorf("the fixed chunk measured %.3f, 512 frames x %.1f frames a second predicts %.3f", before, 300/s0, want)
	}
	// Paced: the clock's worth, plus the lead it prefills (0.05 s of mix) over
	// the run's length.
	if !pacedRatioOK(after, s1) {
		t.Errorf("paced real-time ratio %.3f, want between %.1f and %.3f", after, pacedRatioMin, pacedRatioMax(s1))
	}
	if before > 1.3 && after >= before {
		t.Errorf("pacing did not lower the ratio: %.3f against %.3f", after, before)
	}

	// 3. Without --audio: denied, the game runs silent, no error.
	out = a.must("wctr run --rm --gpu -m 512m quake --map e1m1 --frames 30", 0, "sound off: denied", "audio: silent")
	if _, n, _ := ratio(out); n == 0 {
		t.Error("a silent game painted no mix: its sounds would never be consumed")
	}

	// 4. Detached, with --audio: the page plays the stream.
	a.must("wctr run -d --name qs --gpu --audio -m 512m quake --map e1m1", 0)
	a.until("the page got no sound stream from quake", `() => window.__wctrAudio && window.__wctrAudio.stats().some((s) => s.attached)`, 90*time.Second)
	a.until("the AudioContext is not running", `() => window.__wctrAudio.state() === "running"`, 10*time.Second)
	a.until("quake's stream played nothing", `() => window.__wctrAudio.stats().some((s) => s.played > 0)`, 30*time.Second)
	first := a.streams()[0]
	time.Sleep(2 * time.Second)
	last := a.streams()[0]
	rate := a.contextRate()
	t.Logf("quake stream: context %.0f Hz, %d frames played in 2 s (about %.0f expected), heartbeat %d -> %d, underruns %d, buffered %d",
		rate, last.Played-first.Played, 2*rate, first.Heartbeat, last.Heartbeat, last.Underruns, last.Buffered)
	if got := float64(last.Played - first.Played); got < rate {
		t.Errorf("quake played %.0f frames in 2 s, want at least half of %.0f", got, 2*rate)
	}
	if last.Heartbeat <= first.Heartbeat {
		t.Errorf("the worklet's heartbeat did not move: %d -> %d", first.Heartbeat, last.Heartbeat)
	}
	// The image really mixes sound (it is built with -tags wctraudio, site.go): not
	// only frames of silence reach the worklet. e1m1's ambient sounds are
	// audible at the start; the analyser tap sits after the worklet.
	// ... and keeps reaching it for 60 s (past the 25 s at which the ambient
	// sounds, not looped then, used to end).
	requireSignal(t, "quake", 60, func() float64 {
		return num(a.eval(`() => { const s = window.__wctrAudio.stats()[0]; return s ? s.rms : 0; }`))
	})
	a.must("wctr rm -f qs", 0)
}
