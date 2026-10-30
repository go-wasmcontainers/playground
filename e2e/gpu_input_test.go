//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/go-wasmcontainers/dashboard/ui"
	"github.com/go-wasmcontainers/dashboard/vm"
)

// TestGPUSurfaceInput sends real input to a real GPU guest (E2E_GPU=1, on a
// lane with WebGPU; E2E_GPU_REAL=1 refuses a software adapter): the triangle
// image runs with "input", its surface is a dashboard pane, and a click on
// a known point of the pane and Shift+A typed must reach the guest, which
// logs them -- pointer-down and pointer-up at the surface coordinates the
// pane maps the point to (letterbox included), then KeyA as "A" with
// shift, down and up. The path is the dashboard's ({t:"input"} to this
// page's engine worker), the worker's (webgpu.SurfaceInput) and the
// guest's (wasi-gfx:surface's streams).
func TestGPUSurfaceInput(t *testing.T) {
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
	d := dash{t, page, "chromium/gpu-input", &log}
	d.ready()

	info, _ := page.Evaluate(`async () => {
	  if (!navigator.gpu) return "no navigator.gpu";
	  const a = await navigator.gpu.requestAdapter();
	  if (!a) return "no adapter";
	  const i = a.info || {};
	  const fb = i.isFallbackAdapter || a.isFallbackAdapter;
	  return [i.vendor, i.architecture, i.device, i.description, fb ? "fallback" : ""].join("|");
	}`)
	adapter, _ := info.(string)
	t.Logf("adapter: %s", adapter)
	software := strings.Contains(strings.ToLower(adapter), "swiftshader") || strings.Contains(adapter, "fallback")
	if strings.HasPrefix(adapter, "no ") || software {
		if os.Getenv("E2E_GPU_REAL") == "1" {
			t.Fatalf("no hardware adapter on the GPU lane: %s", adapter)
		}
		t.Skipf("a software adapter (%s): the engine refuses it, so there is no guest to send input to", adapter)
	}

	// The triangle, long enough to be clicked, printing what its pane sends.
	d.run("wctr run -d --gpu --name tri triangle 6000 input")
	d.until("tri running", 120*time.Second, func(p ui.Probe) bool {
		return len(p.Containers) == 1 && strings.Contains(p.Status[1], "1 running")
	})
	d.press("F6", "ArrowDown") // Terminal -> Surfaces
	p := d.until("a frame in the pane", 60*time.Second, func(p ui.Probe) bool {
		return p.View == "Surfaces" && len(p.SurfaceBoxes) == 1 && p.SurfaceBoxes[0].W > 0
	})
	box := p.SurfaceBoxes[0]
	img := vm.Box{X: box.X, Y: box.Y, W: box.W, H: box.H}
	// A known point off the centre (a quarter across, a third down), in the
	// canvas's backing-store pixels; the mouse moves in CSS pixels.
	px, py := box.X+box.W/4, box.Y+box.H/3
	x, y, ok := vm.ToSurface(img, 320, 240, px, py)
	if !ok {
		t.Fatalf("(%d,%d) is not in the frame %+v", px, py, box)
	}
	dpr := 1.0
	if v, err := page.Evaluate(`() => window.devicePixelRatio`); err == nil {
		switch n := v.(type) {
		case float64:
			dpr = n
		case int:
			dpr = float64(n)
		}
	}
	if err := page.Mouse().Click(float64(px)/dpr, float64(py)/dpr); err != nil {
		t.Fatal(err)
	}
	d.press("Shift+KeyA")

	// The guest's log, in the container's detail (Logs follows it).
	want := []string{
		fmt.Sprintf("pointer-down %g,%g", x, y),
		fmt.Sprintf("pointer-up %g,%g", x, y),
		`key-down key-a "A" shift=true`,
		`key-up key-a "A" shift=true`,
	}
	d.press("F6", "ArrowUp", "ArrowUp", "ArrowUp", "ArrowUp", "ArrowUp") // Surfaces -> Containers
	d.until("containers view", 5*time.Second, func(p ui.Probe) bool { return p.View == "Containers" })
	d.press("F6", "ArrowDown") // the table: select tri
	p = d.until("the guest's log", 60*time.Second, func(p ui.Probe) bool { return inOrder(p.Logs, want) })
	t.Logf("guest log:\n%s", strings.TrimSpace(p.Logs))
	d.run("wctr rm -f tri")
}

// inOrder reports whether every line of want appears in s, in order.
func inOrder(s string, want []string) bool {
	for _, w := range want {
		i := strings.Index(s, w)
		if i < 0 {
			return false
		}
		s = s[i+len(w):]
	}
	return true
}
