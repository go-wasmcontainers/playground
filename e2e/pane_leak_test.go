//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-wasmcontainers/dashboard/ui"
	"github.com/mxschmitt/playwright-go"
)

// The engine worker's lifetime probe: devices, buffers, textures made and
// destroyed (GPUDevice.destroy, GPUBuffer.destroy, GPUTexture.destroy), and the
// devices whose lost promise resolved. Installed before the first container, so
// every object of every run is counted.
const lifetimeProbeJS = `() => {
  if (self.__lt) return true;
  const lt = (self.__lt = { dev: 0, devDestroyed: 0, devLost: 0, buf: 0, bufDestroyed: 0, tex: 0, texDestroyed: 0, ctx: 0 });
  if (self.GPUAdapter) {
    const rd = GPUAdapter.prototype.requestDevice;
    GPUAdapter.prototype.requestDevice = async function (...a) {
      const d = await rd.apply(this, a);
      lt.dev++;
      d.lost.then(() => { lt.devLost++; });
      const ds = d.destroy; d.destroy = function () { lt.devDestroyed++; return ds.call(this); };
      const cb = d.createBuffer; d.createBuffer = function (x) { lt.buf++; const b = cb.call(this, x); const bd = b.destroy; b.destroy = function () { lt.bufDestroyed++; return bd.call(this); }; return b; };
      const ct = d.createTexture; d.createTexture = function (x) { lt.tex++; const t = ct.call(this, x); const td = t.destroy; t.destroy = function () { lt.texDestroyed++; return td.call(this); }; return t; };
      return d;
    };
  }
  if (self.OffscreenCanvas) {
    const gc = OffscreenCanvas.prototype.getContext;
    OffscreenCanvas.prototype.getContext = function (k, ...r) { if (k === "webgpu") lt.ctx++; return gc.call(this, k, ...r); };
  }
  self.__ltGet = () => ({ ...lt });
  return true;
}`

// TestPaneRunRmCycles runs the quake guest and removes it, again and again in
// one page, and reports after each cycle what is still alive: the engine
// workers, the devices, buffers and textures it made against those destroyed,
// the canvases and panes of the page, and the guest's frame rate. A container
// that leaves its device, its objects or its pane behind slows the next one
// (go-wasmcontainers/playground#63: the first container of a page runs at 40
// frames a second, the later ones at about 25).
func TestPaneRunRmCycles(t *testing.T) {
	if os.Getenv("E2E_GPU") != "1" || os.Getenv("E2E_URL") != "" {
		t.Skip("set E2E_GPU=1 on a lane with WebGPU")
	}
	if err := os.WriteFile(filepath.Join(siteDir, "gpu-control.js"), gpuControlJS, 0o644); err != nil {
		t.Fatal(err)
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
	d := dash{t, page, "chromium/pane-leak", &log}
	d.ready()
	info, _ := page.Evaluate(`async () => { if (!navigator.gpu) return "no navigator.gpu"; const a = await navigator.gpu.requestAdapter(); if (!a) return "no adapter"; const i = a.info || {}; return [i.vendor, i.architecture, i.device, i.description, (i.isFallbackAdapter || a.isFallbackAdapter) ? "fallback" : ""].join("|"); }`)
	adapter, _ := info.(string)
	if strings.HasPrefix(adapter, "no ") || strings.Contains(strings.ToLower(adapter), "swiftshader") || strings.Contains(adapter, "fallback") {
		if os.Getenv("E2E_GPU_REAL") == "1" {
			t.Fatalf("no hardware adapter on the GPU lane: %s", adapter)
		}
		t.Skipf("a software adapter (%s)", adapter)
	}
	if _, err := page.Evaluate(frameProbeJS); err != nil {
		t.Fatal(err)
	}
	f1 := func(v any) float64 { return num(v) }
	workers := func() (n int, urls string) {
		var u []string
		for _, w := range page.Workers() {
			u = append(u, filepath.Base(strings.SplitN(w.URL(), "?", 2)[0]))
		}
		return len(u), strings.Join(u, ",")
	}
	nw, urls := workers()
	t.Logf("LEAK idle: %d workers (%s)", nw, urls)

	engineWorker := func() playwright.Worker {
		var ew playwright.Worker
		for _, w := range page.Workers() {
			if strings.Contains(w.URL(), "engine-worker") {
				ew = w
			}
		}
		return ew
	}
	// The probe goes in before the first container when the engine worker is
	// already up; otherwise the first cycle's objects are not counted and the
	// log says so.
	probed := false
	if ew := engineWorker(); ew != nil {
		if _, err := ew.Evaluate(lifetimeProbeJS); err != nil {
			t.Fatal(err)
		}
		probed = true
	}
	t.Logf("LEAK engine worker probed before the first container: %v", probed)

	cycles := 5
	var fps []float64
	for i := 1; i <= cycles; i++ {
		// Reversed order: the first two cycles carry the pane lane's flags, the
		// others none. If the flags slow the game, the later cycles are fast; if
		// the position does, every cycle after the first is slow.
		flags := ""
		if i <= 2 {
			flags = "--audio -m 512m --name q quake --slowtics 300 --slowtics-brief"
		} else {
			flags = "-m 512m --name q quake"
		}
		d.run("wctr run -d --gpu " + flags)
		d.until("quake running", 120*time.Second, func(p ui.Probe) bool {
			return len(p.Containers) == 1 && strings.Contains(p.Status[1], "1 running")
		})
		d.press("F6", "ArrowDown") // Terminal -> Surfaces
		d.until("a frame in the pane", 90*time.Second, func(p ui.Probe) bool {
			return p.View == "Surfaces" && len(p.SurfaceBoxes) >= 1 && p.SurfaceBoxes[len(p.SurfaceBoxes)-1].W > 0
		})
		ew := engineWorker()
		if ew == nil {
			t.Fatalf("cycle %d: no engine worker", i)
		}
		if _, err := ew.Evaluate(lifetimeProbeJS); err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
		if _, err := page.Evaluate(`() => window.__frStart()`); err != nil {
			t.Fatal(err)
		}
		time.Sleep(8 * time.Second)
		v, err := page.Evaluate(`() => window.__frStop()`)
		if err != nil {
			t.Fatal(err)
		}
		m, _ := v.(map[string]any)
		fps = append(fps, f1(m["frames"]))
		before := cycleState(t, page, ew, d)
		t.Logf("LEAK cycle %d running: guest %.1f fps, page %.1f rAF/s; %s", i, f1(m["frames"]), f1(m["raf"]), before)
		d.press("F6", "ArrowUp")
		d.run("wctr rm -f q")
		time.Sleep(4 * time.Second)
		after := cycleState(t, page, engineWorker(), d)
		t.Logf("LEAK cycle %d after rm: %s", i, after)
	}
	t.Logf("LEAK guest fps by cycle: %v", fps)
}

// cycleState reads what is alive now: the page's workers and canvases, the
// panes the dashboard shows, and the engine worker's device and object counts.
func cycleState(t *testing.T, page playwright.Page, ew playwright.Worker, d dash) string {
	t.Helper()
	v, _ := page.Evaluate(`() => ({ canvases: document.querySelectorAll("canvas").length, heap: performance.memory ? Math.round(performance.memory.usedJSHeapSize / 1e6) : -1 })`)
	pm, _ := v.(map[string]any)
	s := fmt.Sprintf("%d workers, %.0f canvases, page heap %.0f MB", len(page.Workers()), num(pm["canvases"]), num(pm["heap"]))
	if ew != nil {
		wv, err := ew.Evaluate(`() => self.__ltGet ? self.__ltGet() : null`)
		if err == nil && wv != nil {
			m, _ := wv.(map[string]any)
			s += fmt.Sprintf("; devices %.0f made, %.0f destroyed, %.0f lost; buffers %.0f/%.0f; textures %.0f/%.0f; gpu canvases %.0f",
				num(m["dev"]), num(m["devDestroyed"]), num(m["devLost"]), num(m["buf"]), num(m["bufDestroyed"]), num(m["tex"]), num(m["texDestroyed"]), num(m["ctx"]))
		}
	}
	return s
}
