//go:build e2e

package e2e

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-wasmcontainers/dashboard/ui"
	"github.com/mxschmitt/playwright-go"
)

// TestQuakeSoak plays the quake image the way a person does, for much longer than
// TestQuake's 150 frames (about 3 s): in the dashboard, the pane clicked, W held,
// the mouse moved, space and the mouse button pressed and a spread of other keys,
// for E2E_SOAK_SECONDS (default 45) in each of the two modes (the CPU world, as
// `wctr run --rm --gpu -m 512m quake` runs it, and --gpuworld). The container
// must still be running at the end. A container that stops fails the test with
// its exit status line and the tail of its log: a guest that dies after a few
// seconds of play (a user's report: exit status 134, the engine's trap) passed
// every lane that only ran 150 frames.
//
// Budget: 2 x (45 s of play + about 20 s to start the dashboard and the guest), so
// about 2.5 minutes of a GPU lane; E2E_SOAK_SECONDS=300 for a longer soak by hand.
func TestQuakeSoak(t *testing.T) {
	if os.Getenv("E2E_GPU") != "1" || os.Getenv("E2E_URL") != "" {
		t.Skip("set E2E_GPU=1 on a lane with WebGPU")
	}
	secs := 60
	if s := os.Getenv("E2E_SOAK_SECONDS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			t.Fatalf("E2E_SOAK_SECONDS=%q", s)
		}
		secs = n
	}
	// The image as users run it: the main menu left alone (no --map, no input: a
	// click or Enter would start a game), with and without --audio; and the game.
	// The menu died after 20 to 30 s with "out of memory" until quake-wctr yielded
	// each frame (go-wasmcontainers/playground#56), so the shortest run here
	// is far past that.
	for _, mode := range []struct {
		name, args string
		idle       bool
	}{
		{"menu", "", true},
		{"menu-audio", "", true}, // wctr run --audio
		{"cpu-world", " --map e1m1", false},
		{"gpuworld", " --map e1m1 --gpuworld", false},
	} {
		t.Run(mode.name, func(t *testing.T) {
			runFlags := ""
			if strings.Contains(mode.name, "audio") {
				runFlags = " --audio"
			}
			soak(t, runFlags, mode.args, mode.idle, time.Duration(secs)*time.Second)
		})
	}
}

func soak(t *testing.T, runFlags, flag string, idle bool, play time.Duration) {
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
	d := dash{t, page, "chromium/quake-soak", &log}
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

	d.run("wctr run -d --gpu" + runFlags + " -m 512m --name q quake" + flag)
	d.until("quake running", 120*time.Second, func(p ui.Probe) bool {
		return len(p.Containers) == 1 && strings.Contains(p.Status[1], "1 running")
	})
	d.view("Surfaces")
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
	if !idle { // the first click locks the pointer; in the menu it would act as Enter
		if err := page.Mouse().Click(cx, cy); err != nil {
			t.Fatal(err)
		}
	}
	keys := []string{"KeyA", "KeyS", "KeyD", "KeyQ", "KeyE", "Digit1", "Digit2", "Digit3", "Digit4", "Digit5", "Digit6", "Digit7", "Digit8",
		"ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "ControlLeft", "ShiftLeft", "Tab", "Enter", "Backquote"}
	t0 := time.Now()
	for n := 0; time.Since(t0) < play; n++ {
		if !idle {
			_ = page.Keyboard().Down("KeyW")
			_ = page.Keyboard().Press("Space")
			_ = page.Keyboard().Press(keys[(n*7+3)%len(keys)])
			_ = page.Mouse().Move(cx+float64((n*37)%400)-200, cy+float64((n*53)%300)-150)
			_ = page.Mouse().Click(cx, cy)
		}
		time.Sleep(200 * time.Millisecond)
		if s := d.state(); !strings.Contains(s.Status[1], "1 running") {
			d.view("Containers")
			d.press("F6", "ArrowDown") // select q
			time.Sleep(time.Second)
			s = d.state()
			lg := s.Logs
			if len(lg) > 4000 {
				lg = lg[len(lg)-4000:]
			}
			t.Fatalf("quake%s stopped after %.1f s of play: %q\ncontainers %q\nlog tail:\n%s", flag, time.Since(t0).Seconds(), s.Status, s.Containers, lg)
		}
	}
	_ = page.Keyboard().Up("KeyW")
	t.Logf("quake soak%s: still running after %.0f s of play", flag, time.Since(t0).Seconds())
	d.run("wctr rm -f q")
}
