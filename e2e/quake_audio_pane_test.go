//go:build e2e

package e2e

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-wasmcontainers/dashboard/ui"
	"github.com/mxschmitt/playwright-go"
)

// TestQuakeAudioPane drives the per-container volume and mute in the
// container's dashboard pane (the Vol -, Vol + and Mute buttons of the Surfaces
// view's pane; the page-wide mute is the Sound strip's button) against Quake's
// real sound, on a real GPU (E2E_GPU=1). Two analysers measure the same
// moment: one before any gain (what the worklet played) and one after the
// container's and the page's gains (what reaches the speakers); the pane's
// level is the ratio between them. Quake's menu makes a sound on every
// ArrowDown and ArrowUp, which the test presses to have a signal.
func TestQuakeAudioPane(t *testing.T) {
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
	page, err := browser.NewPage(playwright.BrowserNewPageOptions{Viewport: &playwright.Size{Width: 1280, Height: 800}})
	if err != nil {
		t.Fatal(err)
	}
	var log []string
	page.On("console", func(m playwright.ConsoleMessage) { log = append(log, m.Text()) })
	if _, err := page.Goto(serve(t, true)); err != nil {
		t.Fatal(err)
	}
	d := dash{t, page, "chromium/quake-audio-pane", &log}
	d.ready()

	info, _ := page.Evaluate(`async () => {
	  if (!navigator.gpu) return "no navigator.gpu";
	  const a = await navigator.gpu.requestAdapter();
	  if (!a) return "no adapter";
	  const i = a.info || {};
	  return [i.vendor, i.architecture, i.device, i.description, (i.isFallbackAdapter || a.isFallbackAdapter) ? "fallback" : ""].join("|");
	}`)
	adapter, _ := info.(string)
	if strings.HasPrefix(adapter, "no ") || strings.Contains(strings.ToLower(adapter), "swiftshader") || strings.Contains(adapter, "fallback") {
		if os.Getenv("E2E_GPU_REAL") == "1" {
			t.Fatalf("no hardware adapter on the GPU lane: %s", adapter)
		}
		t.Skipf("SOUND not measured: a software adapter (%s), the engine refuses it and quake cannot draw", adapter)
	}

	// Typing the command is a user gesture (sticky activation), so the page's
	// AudioContext starts as soon as the stream opens.
	// The browser's own animation-frame rate on this runner with the dashboard
	// idle and no game: the guest is paced by the page's acknowledgements, one
	// frame per animation frame, so this is the most frames a second it can have.
	if v, err := page.Evaluate(`() => new Promise((res) => { let n = 0; const t0 = performance.now(); const f = () => { n++; if (performance.now() - t0 < 3000) requestAnimationFrame(f); else res(n / ((performance.now() - t0) / 1000)); }; requestAnimationFrame(f); })`); err == nil {
		t.Logf("PAGE idle, before the game: %.1f animation frames a second", num(v))
	}
	d.run("wctr run -d --gpu --audio -m 512m --name q quake --slowtics 300")
	d.until("quake running", 120*time.Second, func(p ui.Probe) bool {
		return len(p.Containers) == 1 && strings.Contains(p.Status[1], "1 running")
	})
	d.press("F6", "ArrowDown") // Terminal -> Surfaces
	p := d.until("a frame in the pane, with its sound line", 90*time.Second, func(p ui.Probe) bool {
		return p.View == "Surfaces" && len(p.SurfaceBoxes) == 1 && p.SurfaceBoxes[0].W > 0 && len(p.SurfaceSound) == 1
	})
	if p.SurfaceSound[0] != "sound 100%" {
		t.Fatalf("the pane's sound line is %q at the start", p.SurfaceSound[0])
	}
	for _, b := range []string{"surface.1.voldown", "surface.1.volup", "surface.1.mute"} {
		if _, ok := p.Boxes[b]; !ok {
			t.Fatalf("no %s on the pane: %v", b, p.Boxes)
		}
	}
	dpr := 1.0
	if v, err := page.Evaluate(`() => window.devicePixelRatio`); err == nil {
		if n, ok := v.(float64); ok {
			dpr = n
		}
	}
	box := p.SurfaceBoxes[0]
	d.until("the audio stream", 30*time.Second, func(ui.Probe) bool {
		v, _ := page.Evaluate(`() => window.__wctrAudio && window.__wctrAudio.stats().some((s) => s.attached) && window.__wctrAudio.state() === "running"`)
		return v == true
	})
	_, _, _ = box, dpr, page
	testStart := time.Now() // the game has been playing since about now
	// What the user hears is the underruns: counted from here to the end.
	sk := newSoak("chromium/pane", streamReading(t, page))
	watchUnderruns(t, page) // the page's underruns, with their wall-clock times
	var engineWorker, gpuHostWorker playwright.Worker
	for _, w := range page.Workers() {
		switch {
		case strings.Contains(w.URL(), "engine-worker") && strings.Contains(w.URL(), "role=host"):
			gpuHostWorker = w // the GPU host worker (engine#84): same script, role=host
		case strings.Contains(w.URL(), "engine-worker"):
			engineWorker = w
		}
	}
	if engineWorker != nil {
		watchLoopLag(t, engineWorker) // how late the engine worker's event loop runs a 2 ms timer
	}
	if gpuHostWorker != nil {
		watchLoopLag(t, gpuHostWorker) // the same for the thread that serves the GPU calls
	}

	// alive fails the test, with the container's state and the end of its log,
	// when the pane is gone: a game that ends cannot be measured.
	alive := func() {
		if _, ok := d.state().Boxes["surface.1.mute"]; ok {
			return
		}
		d.press("F6", "ArrowUp")
		d.run("clear")
		d.run("wctr ps -a")
		time.Sleep(time.Second)
		d.run("wctr logs q")
		time.Sleep(2 * time.Second)
		t.Fatalf("the quake container is gone from the pane:\n%s", d.state().Terminal)
	}
	// pair reads the tap before the gains and the level after them at once.
	type pair struct{ tap, out float64 }
	pairs := func(secs float64) []pair {
		t.Helper()
		alive()
		deadline := time.Now().Add(time.Duration(secs * float64(time.Second)))
		var got []pair
		for k := 0; time.Now().Before(deadline); k++ {
			key := "ArrowDown"
			if k%2 == 1 {
				key = "ArrowUp"
			}
			// A menu sound: the key goes to the guest as the dashboard sends it
			// ({t:"input", surface, kind, code}), so the pane's focus and the
			// pointer lock stay out of it and the buttons stay clickable.
			if _, err := page.Evaluate(`(code) => { for (const kind of ["key-down", "key-up"]) { window.__wctrEngine.postMessage({ t: "input", surface: 1, kind, code, text: "", alt: false, ctrl: false, meta: false, shift: false }); } return true; }`, key); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 6; i++ {
				v, err := page.Evaluate(`() => { const s = window.__wctrAudio.stats()[0]; return [s ? s.rms : 0, window.__wctrAudio.out().rms]; }`)
				if err != nil {
					t.Fatal(err)
				}
				a := v.([]any)
				got = append(got, pair{num(a[0]), num(a[1])})
				sk.note(streamReading(t, page))
				time.Sleep(60 * time.Millisecond)
			}
		}
		return got
	}
	// loud keeps the readings where the worklet played something (-60 dBFS).
	const floor = 1e-3
	ratio := func(ps []pair) (median float64, n int) {
		var rs []float64
		for _, q := range ps {
			if q.tap > floor {
				rs = append(rs, q.out/q.tap)
			}
		}
		if len(rs) == 0 {
			return -1, 0
		}
		slices.Sort(rs)
		return rs[len(rs)/2], len(rs)
	}
	click := func(name string, times int) {
		t.Helper()
		alive()
		for i := 0; i < times; i++ {
			d.click(name)
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(800 * time.Millisecond) // the 4096-sample window turns over (0.5 s at 8 kHz)
	}
	sound := func() string { return d.state().SurfaceSound[0] }

	// 1. Full volume: what reaches the speakers is what the worklet played.
	full, n := ratio(pairs(2.5))
	t.Logf("full volume: out/tap %.3f over %d readings with sound", full, n)
	if n < 3 {
		t.Fatalf("Quake's menu gave only %d readings above -60 dBFS: no signal to measure with", n)
	}
	if full < 0.9 || full > 1.1 {
		t.Errorf("at full volume out/tap is %.3f, want 1", full)
	}

	// 2. The pane's Vol - seven times: 30 %.
	click("surface.1.voldown", 7)
	if s := sound(); s != "sound 30%" {
		t.Fatalf("after seven Vol -: %q", s)
	}
	if g, _ := page.Evaluate(`() => window.__wctrAudio.containers()[0].gain`); math.Abs(num(g)-0.3) > 1e-6 {
		t.Errorf("the container's gain node is %v, want 0.3", g)
	}
	r30, n := ratio(pairs(2.5))
	t.Logf("30%%: out/tap %.3f over %d readings", r30, n)
	if n < 3 || r30 < 0.25 || r30 > 0.35 {
		t.Errorf("at 30%% the level after the gains is %.3f of the worklet's (%d readings), want 0.30", r30, n)
	}

	// 3. Mute: silence at the speakers while the worklet still plays.
	click("surface.1.mute", 1)
	if s := sound(); s != "sound muted (30%)" {
		t.Fatalf("after Mute: %q", s)
	}
	ps := pairs(2.5)
	var tapMax, outMax float64
	for _, q := range ps {
		tapMax, outMax = max(tapMax, q.tap), max(outMax, q.out)
	}
	t.Logf("muted: worklet up to %.4f, speakers up to %.6f", tapMax, outMax)
	if tapMax < floor {
		t.Errorf("no signal while muted (%.5f): the test cannot show that mute silences it", tapMax)
	}
	if outMax > 1e-4 {
		t.Errorf("muted, yet %.6f (%.1f dBFS) reaches the speakers", outMax, 20*math.Log10(outMax))
	}

	// 4. Unmute: back to 30 %. (The page-wide mute is TestAudio's: this test is
	// kept short because the menu's sounds on its keys do not last.)
	click("surface.1.mute", 1)
	if s := sound(); s != "sound 30%" {
		t.Fatalf("after Unmute: %q", s)
	}
	if r, n := ratio(pairs(2.5)); n < 3 || r < 0.25 || r > 0.35 {
		t.Errorf("after Unmute the level is %.3f of the worklet's (%d readings), want 0.30", r, n)
	}

	// 5. Soak past the window where the guest used to die (about 20 to 33 s: its
	// heap grew until the 512 MiB limit, fixed in go-quake1/engine v0.24.0): the
	// whole test runs at least 60 s, and the pane, the stream and the worklet
	// must be alive at the end, the page still playing (heartbeat moving) and
	// the volume at the pane's level, and Quake's sound must be there in every 5 s
	// window (requireSignal): the ambient sounds loop. The pane's volume is 30 %
	// and the tap is before the gains, so the floor is the worklet's.
	// E2E_TRACE_DIR: a Chromium trace (CDP Tracing) of the page's main thread for
	// the last of the soak and a quiet period after it, kept as a CI artifact. The
	// soak below polls the stream every 10 ms and sends a key every 60 ms; the quiet
	// period does neither, so the two are the page with the test's load and without
	// it (go-wasmcontainers/playground#63: the guest waits in present for the page).
	traceDir := os.Getenv("E2E_TRACE_DIR")
	if traceDir != "" {
		if err := os.MkdirAll(traceDir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(traceDir, "pane-main-thread.json")
		if err := browser.StartTracing(playwright.BrowserStartTracingOptions{
			Page: page, Path: &path,
			// toplevel and devtools.timeline: the tasks and their JS calls; the V8 CPU
			// profile: which wasm (Go) functions fill them; user timing: the marks.
			Categories: []string{"-*", "toplevel", "devtools.timeline", "disabled-by-default-v8.cpu_profiler", "blink.user_timing"},
		}); err != nil {
			t.Fatalf("start tracing: %v", err)
		}
		if _, err := page.Evaluate(`() => performance.mark("polled-soak")`); err != nil {
			t.Fatal(err)
		}
	}
	// What the page's main thread does, as a user's page sees it: the long tasks
	// (over 50 ms, the Long Tasks API) and the animation frames it ran. The guest
	// waits for this thread in present.
	if _, err := page.Evaluate(`() => {
	  const lt = (window.__lt = { n: 0, max: 0, frames: 0, t0: performance.now() });
	  try { new PerformanceObserver((l) => { for (const e of l.getEntries()) { lt.n++; lt.max = Math.max(lt.max, e.duration); } }).observe({ type: "longtask", buffered: false }); } catch (e) {}
	  const tick = () => { lt.frames++; requestAnimationFrame(tick); };
	  requestAnimationFrame(tick);
	  return true;
	}`); err != nil {
		t.Fatal(err)
	}
	pageLoad := func(what string) {
		v, err := page.Evaluate(`() => { const lt = window.__lt, s = (performance.now() - lt.t0) / 1000, r = { n: lt.n, max: lt.max, fps: lt.frames / s, s }; lt.n = 0; lt.max = 0; lt.frames = 0; lt.t0 = performance.now(); return r; }`)
		if err != nil {
			t.Fatal(err)
		}
		m, _ := v.(map[string]any)
		t.Logf("PAGE %s: %.0f s, %.0f animation frames a second, %.0f long tasks over 50 ms, the longest %.0f ms", what, num(m["s"]), num(m["fps"]), num(m["n"]), num(m["max"]))
	}
	polls, createdSteady := 0, -1
	requireSignal(t, "pane", 65-int(time.Since(testStart).Seconds()), func() float64 {
		alive()
		r := streamReading(t, page)
		sk.note(r)
		if polls == 20 && engineWorker != nil {
			createdSteady = takeCreated(t, engineWorker) // start-up is over: a steady frame creates nothing
		}
		if polls++; polls%7 == 1 && len(d.state().Surfaces) == 1 {
			if f, ok := paneFPS(d.state().Surfaces[0]); ok {
				sk.noteFPS(f)
			}
		}
		return r.Rms
	})
	sk.finish(t)
	pageLoad("soak (with the test's polling and keys)")
	if traceDir == "" {
		// The quiet period is also measured without a trace: 20 s of no polling.
		time.Sleep(20 * time.Second)
		pageLoad("quiet (no polling, no keys)")
	}
	if traceDir != "" {
		// 20 s with no polling and no keys: only the page and the game.
		before := streamReading(t, page)
		if _, err := page.Evaluate(`() => performance.mark("quiet-soak")`); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Second)
		after := streamReading(t, page)
		if _, err := page.Evaluate(`() => performance.mark("quiet-end")`); err != nil {
			t.Fatal(err)
		}
		pageLoad("quiet (no polling, no keys)")
		if _, err := browser.StopTracing(); err != nil {
			t.Errorf("stop tracing: %v", err)
		}
		t.Logf("QUIET pane: 20 s with no test polling and no keys: %d underruns (the soak above, with them: %d in %.0f s)", after.Underruns-before.Underruns, sk.result().Underruns, sk.result().Seconds)
	}
	hb := func() float64 {
		v, _ := page.Evaluate(`() => { const s = window.__wctrAudio.stats()[0]; return s ? s.heartbeat : -1; }`)
		return num(v)
	}
	h0 := hb()
	time.Sleep(2 * time.Second)
	if h1 := hb(); h0 < 0 || h1 <= h0 {
		t.Errorf("after %v the worklet's heartbeat is %v -> %v: the page no longer plays", time.Since(testStart).Round(time.Second), h0, h1)
	}
	if s := sound(); s != "sound 30%" {
		t.Errorf("after the soak the pane says %q, want its level kept", s)
	}

	d.press("F6", "ArrowUp") // Surfaces -> Terminal
	// The pane game's own account of its slow tics and dry writes (--slowtics 300
	// --slowtics-brief below), read from the engine's logs endpoint, whole: the
	// terminal is a screen, which lost the lines of the earlier windows. The dry,
	// tight and unfed lines of the soak lane, with the count of what the log held.
	pane := containerLogs(t, page, "q")
	t.Logf("pane game log: %s", paneGameCounts(pane))
	lines := slowTicLines(pane)
	for _, l := range lines {
		t.Log("pane game: " + l)
	}
	evs := takeUnderruns(t, page)
	matchUnderruns(t, evs, parseDryGaps(lines))
	if engineWorker != nil {
		lag, ticks := takeLoopLag(t, engineWorker)
		over := map[int]int{20: 0, 50: 0, 100: 0}
		worst := 0.0
		for _, e := range lag {
			worst = max(worst, e.Late)
			for b := range over {
				if e.Late > float64(b) {
					over[b]++
				}
			}
		}
		t.Logf("LOOPLAG engine worker: %d ticks of 2 ms, %d late by more than 15 ms (>20: %d, >50: %d, >100: %d), worst %.0f ms", ticks, len(lag), over[20], over[50], over[100], worst)
		for _, e := range evs {
			t.Logf("LOOPLAG underrun at %d: worker-loop stalls in the 150 ms before: %v", e.Wall, lagNear(lag, e.Wall))
		}
		calls, logs := takeHostSlow(t, engineWorker), takeBusyLogs(t, engineWorker, gpuHostWorker)
		for _, l := range hostSlowReport(calls, 40, takeBatchSlow(t, engineWorker)...) {
			t.Log("HOSTCALL " + l)
		}
		for _, l := range busyReport(calls, logs, 15) {
			t.Log("BUSY " + l)
		}
		// The GC question: of the engine worker's slices of 10 ms or more, the ones a
		// collection ran in and the others, with the time under no label.
		t.Log("BUSY " + gcSplitLine(logs.Engine, 10))
		t.Log("BUSY gpu host worker " + gcSplitLine(logs.Host, 10))
		t.Log("PRESENT " + presentSlowLine(takePresentSlow(t, engineWorker)))
		if createdSteady >= 0 {
			end := takeCreated(t, engineWorker)
			t.Logf("CREATED %d objects by poll 20, %d at the end", createdSteady, end)
			if msg := steadyCreatesMsg(createdSteady, end); msg != "" {
				t.Error("CREATED " + msg)
			}
		}
		lags := loopLags{Engine: lag}
		if gpuHostWorker != nil {
			var hticks int
			lags.Host, hticks = takeLoopLag(t, gpuHostWorker)
			t.Logf("LOOPLAG gpu host worker: %d ticks of 2 ms, %d late by more than 15 ms", hticks, len(lags.Host))
		}
		for _, l := range waitHoles(calls, logs, lags) {
			t.Log("BUSY " + l)
		}
		if msg, bad := longWaitsLackLabels(calls, logs); bad {
			t.Error("BUSY " + msg)
		}
	}
	d.run("wctr rm -f q")
}
