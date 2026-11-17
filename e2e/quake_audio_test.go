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

// The paced mixer, judged by what it was asked to do. The raw real-time ratio
// (mix frames offered per WALL second over 11025) falls below 1 on a slow
// runner, because a tic longer than the mix-ahead (0.1 s) is not paid back (a
// stall is not a debt, S_Update): a looser lower bound would only hide that, and
// would not tell a slow game from a mixer that under-produces. The game's
// summary therefore also prints the mix the stalled tics were owed and dropped
// (go-quake1/engine#133): offered plus dropped is the whole of the game's time,
// so "ratio with them" is 1.00 whatever the machine, and it is that which is
// asserted. The upper bound is the old defect: the fixed 512-frame chunk reads
// 2.7 to 2.8 at 60 frames a second, far above 1.1.
func pacedRatioMax(seconds float64) float64 { return 1 + 0.06/seconds + 0.08 }

// pacedAccountedMin: what the paced mixer offers plus drops, over the game's
// time. Under it the mixer under-produced; the 3 % are the run's first tic (it
// has no dt) and the rounding.
const pacedAccountedMin = 0.97

func pacedRatioOK(ratio, withStalls, seconds float64) bool {
	return withStalls >= pacedAccountedMin && withStalls <= pacedRatioMax(seconds) && ratio <= pacedRatioMax(seconds)
}

// stallsLine reads what the game says about its stalls after the ratio. The
// terminal wraps a long line (at about 120 columns, in the middle of it): the
// breaks are taken out first.
func stallsLine(out string) (fps float64, dropped, stalls int, withStalls float64, ok bool) {
	out = strings.NewReplacer("\r", "", "\n", "").Replace(out)
	m := regexp.MustCompile(`real-time ratio [0-9.]+; ([0-9.]+) frames a second, (\d+) mix frames dropped in (\d+) stalled tics, (?:\d+ prepaid by the queue, )?ratio with them ([0-9.]+)`).FindStringSubmatch(out)
	if m == nil {
		return 0, 0, 0, 0, false
	}
	fps, _ = strconv.ParseFloat(m[1], 64)
	dropped, _ = strconv.Atoi(m[2])
	stalls, _ = strconv.Atoi(m[3])
	withStalls, _ = strconv.ParseFloat(m[4], 64)
	return fps, dropped, stalls, withStalls, true
}

// slowTicLines are the game's report of every tic over 0.1 s (go-quake1/engine
// #139): its stages, the time outside the frame, the sound write and the
// collector, and the closing share of the slow tics' time each holds. The
// terminal wraps lines, so the text is joined and cut at each "slowtic".
func slowTicLines(out string) []string {
	out = strings.NewReplacer("\r", "", "\n", "").Replace(out)
	var lines []string
	for _, m := range regexp.MustCompile(`slowtic(?: frame=\d+ [^;]*?gc-pause-ms=[0-9.]+|s: \d+ over .*?gc-pause [0-9.]+%|s: none over [0-9]+ ms in \d+ frames)|audiogap frame=\d+ gap-ms=[0-9.]+ queued-ms=[0-9.]+ short-ms=[0-9.]+|audiogaps: \d+ of \d+ writes ran dry[^;]*?; writes more than 50 100 200 ms after the one before: \d+ \d+ \d+|yieldshare: of the \d+ ms the slow tics spent in the yield, collector background mark \d+ ms \(\d+%\), other goroutines \d+ ms \(\d+%\)|yieldwho: of the other goroutines' \d+ ms: scavenger \d+ ms; \d+ cleanups and \d+ finalizers ran in those yields \(\d+ cleanups in the whole frames of the slow tics\); sweeper and the rest \d+ ms`).FindAllString(out, -1) {
		lines = append(lines, m)
	}
	return lines
}

// goroutineDump is the game's dump of its goroutines (go-quake1/engine, printed
// once after the report), as it came: the terminal wraps its lines, so it is kept
// whole, from its first line to the end of the output, cut at 6000 bytes.
func goroutineDump(out string) string {
	i := strings.Index(out, "goroutines: goroutine profile")
	if i < 0 {
		return ""
	}
	return out[i:min(len(out), i+6000)]
}

// ticTail is what the game says about its tic lengths and the mix its queue had
// already paid (go-quake1/engine#136), for the log: the distribution a ratio and
// an underrun count are read against. Empty on an engine without it.
func ticTail(out string) string {
	out = strings.NewReplacer("\r", "", "\n", "").Replace(out)
	m := regexp.MustCompile(`(?:(\d+ prepaid by the queue)[^;]*)?; (tics over [0-9. ]+s: \d+ \d+ \d+ \d+, longest [0-9.]+ s)`).FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	if m[1] == "" { // engine v0.43.0 and later: no prepaid mix any more
		return m[2]
	}
	return m[1] + "; " + m[2]
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
// 2.2 at 48, 1.3 at 28, 0.86 at 15: it drops nothing, so it fails there too)
// fails; the idle machine (1.01) and a slow runner whose stalls account for the
// gap (0.746 with 0.25 dropped) pass; a mixer that under-produces (0.55 with
// nothing dropped) fails.
func TestPacedRatioBounds(t *testing.T) {
	for _, c := range []struct {
		name              string
		ratio, withStalls float64
		ok                bool
	}{
		{"fixed chunk, 60 fps", 2.75, 2.75, false}, {"fixed chunk, 48 fps", 2.2, 2.2, false},
		{"fixed chunk, 28 fps", 1.3, 1.3, false}, {"fixed chunk, 15 fps", 0.861, 0.861, false},
		{"idle machine", 1.01, 1.01, true}, {"slow runner, stalls dropped 0.25", 0.746, 0.996, true},
		{"slow runner, stalls dropped 0.116", 0.884, 1.0, true},
		{"under-producing mixer", 0.55, 0.55, false}, {"nothing", 0, 0, false},
	} {
		if got := pacedRatioOK(c.ratio, c.withStalls, 5); got != c.ok {
			t.Errorf("%s: ratio %.3f (%.3f with the stalls): accepted=%v, want %v", c.name, c.ratio, c.withStalls, got, c.ok)
		}
	}
	// The wrapped line, as the terminal printed it on CI (a break after "in 7 ").
	if _, _, _, w, ok := stallsLine("audio: playing, 1 mix frames offered, 1 accepted, in 4.91 s: real-time ratio 0.746; 15.0 frames a second, 13794 mix frames dropped in 7 \nstalled tics, ratio with them 0.996\n"); !ok || w != 0.996 {
		t.Errorf("stallsLine did not read the engine's line: %v %v", w, ok)
	}
	// The line of engine v0.31.0 and later, wrapped by the terminal as well.
	full := "audio: playing, 1 mix frames offered, 1 accepted, in 4.91 s: real-time ratio 0.746; 15.0 frames a second, 13794 mix frames dropped in 7 stalled tics, 900 prepaid by the \nqueue, ratio with them 1.004; tics over 0.1 0.2 0.35 0.5 s: 9 5 3 1, longest 0.620 s\n"
	if _, _, _, w, ok := stallsLine(full); !ok || w != 1.004 {
		t.Errorf("stallsLine did not read the line with the prepaid mix: %v %v", w, ok)
	}
	// The slow-tic report, wrapped by the terminal: one line per tic and the share.
	slow := "slowtic frame=5 interval-ms=400.0 sim=20.0 world=50.0 gpu=200.0 copy=50.0 models=0.0 composite=10.0 present=20.0 unstaged=30.0 outside=20.\n0 audio-write=4.00 gc-cycles=2 gc-pause-ms=3.00\nslowtic frame=9 interval-ms=150.0 sim=1.0 world=1.0 gpu=1.0 copy=1.0 models=1.0 composite=1.0 present=1.0 unstaged=1.0 outside=140.0 audio-write=0.00 gc-cycles=0 gc-pause-ms=0.00\nslowtics: 2 over 100 ms, 550 ms in all; share of that time: sim 4% world 9% outside 29%; of it audio-write 0.7% gc-pause 0.5%\n"
	if ls := slowTicLines(slow); len(ls) != 3 || !strings.HasSuffix(ls[0], "gc-pause-ms=3.00") || !strings.HasPrefix(ls[2], "slowtics: 2 over") || !strings.HasSuffix(ls[2], "gc-pause 0.5%") {
		t.Errorf("slowTicLines = %q", ls)
	}
	gaps := "audiogap frame=7 gap-ms=250.0 queued-ms=80.0 short-ms=170.0\naudiogap frame=11 gap-ms=130.0 queued-ms=50.0 short-ms=80.\n0\naudiogaps: 2 of 40 writes ran dry, 250 ms short in all, worst 170.0 ms, median queue before a dry gap 65.0 ms; writes more than 50 100 200 ms after the one before: 9 3 1\n"
	if ls := slowTicLines(gaps); len(ls) != 3 || !strings.HasSuffix(ls[1], "short-ms=80.0") || !strings.HasSuffix(ls[2], "after the one before: 9 3 1") {
		t.Errorf("slowTicLines on the audio gaps = %q", ls)
	}
	// The line of the reference mixahead (no prepaid mix).
	noPrepaid := "audio: playing, 1 mix frames offered, 1 accepted, in 4.91 s: real-time ratio 0.99; 15.0 frames a second, 13794 mix frames dropped in 7 stalled tics, ratio with them 1.004; tics over 0.1 0.2 0.35 0.5 s: 9 5 3 1, longest 0.620 s\n"
	if _, _, _, w, ok := stallsLine(noPrepaid); !ok || w != 1.004 {
		t.Errorf("stallsLine did not read the line without prepaid mix: %v %v", w, ok)
	}
	if got, want := ticTail(noPrepaid), "tics over 0.1 0.2 0.35 0.5 s: 9 5 3 1, longest 0.620 s"; got != want {
		t.Errorf("ticTail without prepaid = %q, want %q", got, want)
	}
	if got, want := ticTail(full), "900 prepaid by the queue; tics over 0.1 0.2 0.35 0.5 s: 9 5 3 1, longest 0.620 s"; got != want {
		t.Errorf("ticTail = %q, want %q", got, want)
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
		out = strings.NewReplacer("\r", "", "\n", "").Replace(out) // the terminal wraps long lines
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
	gameFPS, dropped, stalls, withStalls, ok := stallsLine(out)
	if !ok {
		t.Fatalf("the game's audio line has no stall accounting (go-quake1/engine#133):\n%s", out)
	}
	t.Logf("paced run: game %.1f fps, ratio %.3f, %d mix frames dropped in %d stalled tics, ratio with them %.3f; %s", gameFPS, after, dropped, stalls, withStalls, ticTail(out))
	for _, l := range slowTicLines(out) {
		t.Log(l)
	}
	if d := goroutineDump(out); d != "" {
		t.Logf("the game's goroutines:\n%s", d)
	}
	if m := regexp.MustCompile(`phases over \d+ frames: [^$]*?gpu-max-ms=[0-9.]+`).FindString(strings.NewReplacer("\r", "", "\n", "").Replace(out)); m != "" {
		t.Log(m) // the per-phase means of the whole run, beside the slow tics
	}
	if !pacedRatioOK(after, withStalls, s1) {
		t.Errorf("paced mixer: ratio %.3f, with the %d frames its %d stalled tics dropped %.3f, want %.2f to %.3f: it under-produced or over-produced, which a slow game does not explain",
			after, dropped, stalls, withStalls, pacedAccountedMin, pacedRatioMax(s1))
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
	a.must("wctr run -d --name qs --gpu --audio -m 512m quake --map e1m1 --slowtics 300", 0)
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
	sk := newSoak("chromium/quake", last)
	sk.noteFPS(gameFPS)
	requireSignal(t, "quake", 60, func() float64 {
		r := a.streams()[0]
		sk.note(r)
		return r.Rms
	})
	sk.finish(t)
	// The same session's own account of its slow tics (--slowtics 300 below): the
	// game's windows of 300 frames, beside the underruns just counted.
	logs := a.must("wctr logs qs", 0)
	t.Logf("the soak game's slow tics, window by window (%d windows):", strings.Count(logs, "slowtics:")+strings.Count(logs, "slowtics: none"))
	for _, l := range slowTicLines(logs) {
		t.Log(l)
	}
	// The soak game has more goroutines than the 300-frame run (8 against 1): the
	// first window's dump names them.
	if d := goroutineDump(logs); d != "" {
		t.Logf("the soak game's goroutines:\n%s", d)
	}
	a.must("wctr rm -f qs", 0)
}
