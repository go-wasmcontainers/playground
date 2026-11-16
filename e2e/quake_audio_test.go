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
	// The lower bound is looser than the upper: a tic longer than the mix-ahead
	// (0.1 s) is not paid back (a stall is not a debt), so a loaded runner,
	// which CI's macOS lane is (about 15 frames a second: 0.884 measured), mixes
	// a little under real time while an idle machine measures 1.01.
	if max := 1 + 0.06/s1 + 0.08; after > max || after < 0.8 {
		t.Errorf("paced real-time ratio %.3f, want between 0.8 and %.3f", after, max)
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
	best := -200.0
	for i := 0; i < 200 && best < -80; i++ {
		for _, s := range a.streams() {
			if s.RmsDb > best {
				best = s.RmsDb
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("quake's loudest tap reading: %.1f dBFS", best)
	if best < -80 {
		t.Errorf("only silence reached the worklet from the quake image (%.1f dBFS)", best)
	}
	a.must("wctr rm -f qs", 0)
}
