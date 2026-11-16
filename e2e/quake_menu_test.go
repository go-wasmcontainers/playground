//go:build e2e

package e2e

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-wasmcontainers/dashboard/ui"
	"github.com/mxschmitt/playwright-go"

	"github.com/go-wasmcontainers/playground/internal/framediff"
)

// TestQuakeMenuMouse plays the quake image as it starts by default, in the
// dashboard, on a real GPU (E2E_GPU=1): it boots into the main menu; the
// first click on the pane locks the pointer (and is not a menu click); Enter
// three times (Single Player, New Game, a skill) starts the game; then the
// mouse turns the view, and the frame changes in the direction the mouse
// says: a motion to the right moves the scene to the left, a motion to the
// left moves it to the right.
//
// It runs once for each world the engine draws with (the GPU's, the default
// since engine v0.22.0, and --cpuworld). Every wait is on something the page
// shows, never a sleep that guesses how long the guest takes; and a guest that
// exits is reported as that (its status), not as a scene that did not move.
func TestQuakeMenuMouse(t *testing.T) {
	if os.Getenv("E2E_GPU") != "1" || os.Getenv("E2E_URL") != "" {
		t.Skip("set E2E_GPU=1 on a lane with WebGPU")
	}
	for _, mode := range []struct{ name, flag string }{{"cpuworld", " --cpuworld"}, {"gpuworld", ""}} {
		t.Run(mode.name, func(t *testing.T) {
			// The GPU world runs out of memory in the start hub, in most runs,
			// since engine v0.20.0 (go-quake1/engine#123): the guest exits a few
			// seconds into the game. Run with E2E_QUAKE_GPUWORLD=1 to see it; drop
			// this skip when the issue is closed.
			if mode.flag == "" && os.Getenv("E2E_QUAKE_GPUWORLD") != "1" {
				t.Skip("go-quake1/engine#123: the GPU world exhausts memory in the start hub (E2E_QUAKE_GPUWORLD=1 runs it)")
			}
			quakeMenuMouse(t, mode.flag)
		})
	}
}

func quakeMenuMouse(t *testing.T, flag string) {
	pw, err := playwright.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Stop()
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true), Channel: playwright.String("chromium"),
		Args:    append([]string{"--enable-unsafe-webgpu"}, strings.Fields(os.Getenv("E2E_GPU_ARGS"))...),
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
	d := dash{t, page, "chromium/quake-menu", &log}
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
		t.Skipf("a software adapter (%s): the engine refuses it", adapter)
	}

	// What the page learns about the guest's wish and the browser's answer,
	// for the failure message.
	if _, err := page.Evaluate(`() => {
	  window.__caps = [];
	  window.__wctrEngine.addEventListener("message", (e) => { if (e.data && e.data.op === "capture") window.__caps.push(e.data); });
	  document.addEventListener("pointerlockerror", () => { window.__lockError = true; });
	}`); err != nil {
		t.Fatal(err)
	}

	// No flags: the image's default, the main menu.
	d.run("wctr run -d --gpu -m 512m --name q quake" + flag)
	d.until("quake running", 120*time.Second, func(p ui.Probe) bool {
		return len(p.Containers) == 1 && strings.Contains(p.Status[1], "1 running")
	})
	d.press("F6", "ArrowDown") // Terminal -> Surfaces
	p := d.until("a frame in the pane", 60*time.Second, func(p ui.Probe) bool {
		return p.View == "Surfaces" && len(p.SurfaceBoxes) == 1 && p.SurfaceBoxes[0].W > 0
	})
	box := p.SurfaceBoxes[0]
	dpr := 1.0
	if v, err := page.Evaluate(`() => window.devicePixelRatio`); err == nil {
		if n, ok := v.(float64); ok {
			dpr = n
		}
	}
	cx, cy := float64(box.X+box.W/2)/dpr, float64(box.Y+box.H/2)/dpr

	// A band of the pane across the middle of the frame: RGBA, canvas pixels.
	bandH := 80
	band := func() []byte {
		t.Helper()
		v, err := page.Evaluate(`([x, y, w, h]) => Array.from(document.getElementById("screen").getContext("2d").getImageData(x, y, w, h).data)`,
			[]int{box.X, box.Y + box.H/2 - bandH/2, box.W, bandH})
		if err != nil {
			t.Fatal(err)
		}
		a := v.([]any)
		out := make([]byte, len(a))
		for i, x := range a {
			switch n := x.(type) {
			case int:
				out[i] = byte(n)
			case float64:
				out[i] = byte(n)
			}
		}
		return out
	}
	locked := func() bool {
		v, err := page.Evaluate(`() => document.pointerLockElement === document.getElementById("screen")`)
		if err != nil {
			t.Fatal(err)
		}
		return v.(bool)
	}
	// turn sends a mouse motion as the browser reports one while the pointer
	// is locked: n events of movementX = dx spread over about 1.5 s, a few a
	// frame (the pane sums them per frame).
	turn := func(dx, n int) {
		t.Helper()
		if _, err := page.Evaluate(`async ([dx, n]) => {
		  const c = document.getElementById("screen");
		  for (let i = 0; i < n; i++) {
		    c.dispatchEvent(new MouseEvent("mousemove", { movementX: dx, movementY: 0, bubbles: true }));
		    await new Promise((r) => setTimeout(r, 30));
		  }
		}`, []int{dx, n}); err != nil {
			t.Fatal(err)
		}
	}
	// requireAlive fails the test, naming the cause, when the guest has
	// exited: the pane then shows its last frame greyed and nothing moves, so
	// every later assertion would blame the mouse (engine#123: the GPU world
	// ran out of memory in the start hub, exit status 2).
	requireAlive := func(what string) {
		t.Helper()
		if s := d.state(); !strings.Contains(s.Status[1], "1 running") {
			t.Fatalf("the guest exited %s: surfaces %q, status %q", what, s.Surfaces, s.Status)
		}
	}
	// changed waits until the pane's band differs from before (the menu moved
	// on, the view turned), and returns it; settled then waits until two
	// frames 150 ms apart are the same (the scene is at rest).
	// look is the whole pane, one pixel in four each way: a menu screen, or a
	// turn of the view, changes it where the middle band alone might not.
	look := func() []byte {
		t.Helper()
		v, err := page.Evaluate(`([x, y, w, h]) => {
		  const d = document.getElementById("screen").getContext("2d").getImageData(x, y, w, h).data, out = [];
		  for (let j = 0; j < h; j += 4) for (let i = 0; i < w; i += 4) { const k = 4 * (j * w + i); out.push(d[k], d[k + 1], d[k + 2]); }
		  return out;
		}`, []int{box.X, box.Y, box.W, box.H})
		if err != nil {
			t.Fatal(err)
		}
		a := v.([]any)
		out := make([]byte, len(a))
		for i, x := range a {
			switch n := x.(type) {
			case int:
				out[i] = byte(n)
			case float64:
				out[i] = byte(n)
			}
		}
		return out
	}
	changed := func(what string, before []byte, over float64) []byte {
		t.Helper()
		var now []byte
		d.until(what, 20*time.Second, func(ui.Probe) bool {
			now = look()
			diff, err := framediff.MeanAbsDiff(now, before)
			if err != nil {
				t.Fatal(err)
			}
			return diff > over
		})
		return now
	}
	settled := func(what string) []byte {
		t.Helper()
		prev := look()
		var now []byte
		d.until(what, 20*time.Second, func(ui.Probe) bool {
			time.Sleep(150 * time.Millisecond)
			now = look()
			diff, err := framediff.MeanAbsDiff(now, prev)
			if err != nil {
				t.Fatal(err)
			}
			prev = now
			return diff < 1.5 // lights flicker, a few levels of grey; a turn is tens
		})
		return now
	}

	// The guest asked for the lock when it opened its surface, before its first
	// frame: the pane knows by now. One click locks the pointer.
	if locked() {
		t.Fatal("the pointer is locked before any click")
	}
	if err := page.Mouse().Click(cx, cy); err != nil {
		t.Fatal(err)
	}
	why := func() string {
		v, _ := page.Evaluate(`([x, y]) => { const e = document.elementFromPoint(x, y); return JSON.stringify({ wishes: window.__caps, lockError: window.__lockError === true, at: e && (e.tagName + "#" + e.id + "." + e.className), screen: document.getElementById("screen") === e, canvas: [document.getElementById("screen").width, document.getElementById("screen").height], rect: JSON.stringify(document.getElementById("screen").getBoundingClientRect()), focus: document.hasFocus(), pl: String(document.pointerLockElement) }) }`, []float64{cx, cy})
		s, _ := v.(string)
		return s
	}
	d.until("the pointer lock ("+why()+")", 10*time.Second, func(ui.Probe) bool { return locked() })

	// The menu is up: the click that grabbed the mouse did not press a row, so
	// three Enters are Single Player, New Game, and a skill.
	// Each Enter shows the next screen of the menu, and the last one the game:
	// wait for the pane to change after each, never a fixed time.
	menu := look()
	for i := 0; i < 3; i++ {
		requireAlive("in the menu")
		if err := page.Keyboard().Press("Enter"); err != nil {
			t.Fatal(err)
		}
		menu = changed("the next menu screen, or the game", menu, 0.2) // a few words of text on black
	}
	requireAlive("starting the game")
	settled("the game at rest")

	shift := func(what string, before, after []byte) int {
		t.Helper()
		s, err := framediff.Shift(before, after, box.W, bandH, box.W/3)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: the scene moved %d px (pane %d px wide)", what, s, box.W)
		return s
	}
	start, before := band(), look()
	turn(12, 50) // 600 surface pixels to the right: about 13 degrees of yaw
	requireAlive("turning right")
	changed("the view turning right", before, 4)
	settled("the view at rest after turning right")
	right := band()
	if s := shift("mouse to the right", start, right); s >= -5 {
		t.Errorf("mouse to the right: the scene moved %d px, want it to move left by more than 5", s)
	}
	before = look()
	turn(-12, 100) // twice as far back, to the left
	requireAlive("turning left")
	changed("the view turning left", before, 4)
	settled("the view at rest after turning left")
	left := band()
	if s := shift("mouse to the left", right, left); s <= 5 {
		t.Errorf("mouse to the left: the scene moved %d px, want it to move right by more than 5", s)
	}

	d.press("F6", "ArrowUp") // Surfaces -> Terminal
	if t.Failed() {
		st := d.state()
		t.Logf("surfaces %q containers %q status %q", st.Surfaces, st.Containers, st.Status)
		d.press("F6", "ArrowUp", "ArrowUp", "ArrowUp", "ArrowUp", "ArrowUp") // Surfaces -> Containers
		d.press("F6", "ArrowDown")                                           // select q
		time.Sleep(time.Second)
		lg := d.state().Logs
		if len(lg) > 6000 {
			lg = lg[len(lg)-6000:]
		}
		t.Logf("container log:\n%s", lg)
	}
	d.run("wctr rm -f q")
}
