//go:build e2e

package e2e

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-wasmcontainers/dashboard/ui"
	"github.com/mxschmitt/playwright-go"
)

//go:embed testdata/gpu_control.js
var gpuControlJS []byte

// The page-side probes: when the engine posts a frame, when the page
// acknowledges it, the animation-frame times, all with performance.now().
const frameProbeJS = `() => {
  if (window.__fr) return true;
  const fr = (window.__fr = { recv: new Map(), lat: [], ack: [], recvT: [], raf: [], on: false, w: 0, h: 0, t0: 0 });
  window.__wctrEngine.addEventListener("message", (e) => {
    const m = e.data;
    if (fr.on && m && m.t === "frame") { const t = performance.now(); fr.recv.set(m.seq, t); fr.recvT.push(t); fr.w = m.width || fr.w; fr.h = m.height || fr.h; }
  });
  const post = Worker.prototype.postMessage;
  Worker.prototype.postMessage = function (m, ...r) {
    if (fr.on && m && m.t === "frame-ack") { const t = performance.now(); fr.ack.push(t); const r0 = fr.recv.get(m.seq); if (r0 !== undefined) { fr.lat.push(t - r0); fr.recv.delete(m.seq); } }
    return post.call(this, m, ...r);
  };
  const tick = (t) => { if (fr.on) fr.raf.push(t); requestAnimationFrame(tick); };
  requestAnimationFrame(tick);
  const stat = (a) => { if (!a.length) return { mean: 0, p95: 0, max: 0 }; const b = [...a].sort((x, y) => x - y); return { mean: a.reduce((s, v) => s + v, 0) / a.length, p95: b[Math.min(b.length - 1, Math.floor(0.95 * b.length))], max: b[b.length - 1] }; };
  const iv = (a) => { const d = []; for (let i = 1; i < a.length; i++) d.push(a[i] - a[i - 1]); return d; };
  window.__frStart = () => { fr.recv.clear(); fr.lat = []; fr.ack = []; fr.recvT = []; fr.raf = []; fr.t0 = performance.now(); fr.on = true; };
  window.__frStop = () => {
    fr.on = false;
    const s = (performance.now() - fr.t0) / 1000;
    const ri = stat(iv(fr.raf)), fi = stat(iv(fr.recvT));
    return { s, raf: fr.raf.length / s, frames: fr.recvT.length / s, acks: fr.ack.length / s, lat: stat(fr.lat), rafInt: ri, frameInt: fi, w: fr.w, h: fr.h };
  };
  return true;
}`

// The engine worker's probes: when each submit's work completes
// (onSubmittedWorkDone), what transferToImageBitmap costs, when it posts frames.
const workerProbeJS = `() => {
  if (self.__wf) return true;
  const wf = (self.__wf = { done: [], xfer: [], post: [], subm: 0, on: false, t0: 0, stacks: {} });
  if (self.GPUQueue) {
    const sub = GPUQueue.prototype.submit;
    GPUQueue.prototype.submit = function (c) {
      if (self.__noKick && c && c.length === 0) return undefined;
      const t = performance.now();
      const r = sub.call(this, c);
      if (wf.on) { wf.subm++; const k = (c.length || 0) + " cb " + String(new Error().stack).split("\n").slice(2, 6).map((l) => l.trim().replace(/^at /, "").replace(/\(.*\/|\)$/g, "")).join(" < "); wf.stacks[k] = (wf.stacks[k] || 0) + 1; this.onSubmittedWorkDone().then(() => { wf.done.push(performance.now() - t); }); }
      return r;
    };
  }
  if (self.OffscreenCanvas) {
    const x = OffscreenCanvas.prototype.transferToImageBitmap;
    OffscreenCanvas.prototype.transferToImageBitmap = function () { const t = performance.now(); const r = x.call(this); if (wf.on) wf.xfer.push(performance.now() - t); return r; };
  }
  const pm = self.postMessage;
  self.postMessage = function (m, ...r) { if (wf.on && m && m.t === "frame") wf.post.push(performance.now()); return pm.call(this, m, ...r); };
  const stat = (a) => { if (!a.length) return { mean: 0, p95: 0, max: 0 }; const b = [...a].sort((x, y) => x - y); return { mean: a.reduce((s, v) => s + v, 0) / a.length, p95: b[Math.min(b.length - 1, Math.floor(0.95 * b.length))], max: b[b.length - 1] }; };
  self.__wfStart = () => { wf.done = []; wf.xfer = []; wf.post = []; wf.stacks = {}; wf.subm = 0; wf.t0 = performance.now(); wf.on = true; };
  self.__wfStop = () => { wf.on = false; const s = (performance.now() - wf.t0) / 1000; return { s, submits: wf.subm / s, done: stat(wf.done), xfer: stat(wf.xfer), posted: wf.post.length / s, stacks: wf.stacks }; };
  return true;
}`

// The JS controls: a GPU worker (gpu_control.js) and a main-thread canvas upload
// like the dashboard's (a full 2D canvas put each animation frame).
const controlJS = `() => {
  if (window.__ctl) return true;
  const c = (window.__ctl = { worker: null, iters: 0, s: 0, canvas: null, uploading: false, bitmaps: 0 });
  c.startGPU = (w, h, loops, bitmaps) => new Promise((res) => {
    c.worker = new Worker("gpu-control.js");
    c.bitmaps = 0;
    c.worker.onmessage = (e) => {
      if (e.data instanceof ImageBitmap) { c.bitmaps++; if (c.canvas) c.canvas.getContext("2d").drawImage(e.data, 0, 0); e.data.close(); return; }
      if (e.data && e.data.done) { c.iters = e.data.iters; c.s = e.data.s; c.resolve && c.resolve({ iters: c.iters, s: c.s, perSec: c.iters / c.s, bitmaps: c.bitmaps }); }
    };
    c.worker.postMessage({ w, h, loops, bitmaps });
    setTimeout(res, 1500); // device and pipeline are up
  });
  c.stopGPU = () => new Promise((res) => { c.resolve = res; c.worker.postMessage("stop"); });
  c.startUpload = (w, h) => {
    if (!c.canvas) { c.canvas = document.createElement("canvas"); c.canvas.style.cssText = "position:fixed;left:0;top:0;width:" + w + "px;height:" + h + "px;opacity:0.01;pointer-events:none"; document.body.appendChild(c.canvas); }
    c.canvas.width = w; c.canvas.height = h;
    const ctx = c.canvas.getContext("2d"), img = ctx.createImageData(w, h);
    c.uploading = true;
    const f = () => { if (!c.uploading) return; img.data[0] = (img.data[0] + 1) & 255; ctx.putImageData(img, 0, 0); requestAnimationFrame(f); };
    requestAnimationFrame(f);
  };
  c.stopUpload = () => { c.uploading = false; };
  return true;
}`

// TestPaneFrameRate measures where a guest frame's time goes between its submit
// and the page's acknowledgement, and what the controls do to the browser's
// animation-frame rate (go-wasmcontainers/playground#63: 40 frames a second
// idle, 15 with the pane game). One browser, one runner, in turn:
//
//	idle             the dashboard, no game
//	guest, pane      the quake guest, its pane shown
//	guest, hidden    the same, the Surfaces view not shown
//	GPU control      the WebGPU render + read-back + ImageBitmap loop of gpu_control.js, no guest
//	upload control   a full-size 2D canvas put every animation frame, no guest
//	both controls    the two together, the bitmaps drawn into the canvas
//
// If the controls also bring the animation frames to about 15 a second, the
// runner's GPU (or compositor) is the limit; if not, the difference is the guest's.
func TestPaneFrameRate(t *testing.T) {
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
	d := dash{t, page, "chromium/pane-framerate", &log}
	d.ready()
	info, _ := page.Evaluate(`async () => { if (!navigator.gpu) return "no navigator.gpu"; const a = await navigator.gpu.requestAdapter(); if (!a) return "no adapter"; const i = a.info || {}; return [i.vendor, i.architecture, i.device, i.description, (i.isFallbackAdapter || a.isFallbackAdapter) ? "fallback" : ""].join("|"); }`)
	adapter, _ := info.(string)
	if strings.HasPrefix(adapter, "no ") || strings.Contains(strings.ToLower(adapter), "swiftshader") || strings.Contains(adapter, "fallback") {
		if os.Getenv("E2E_GPU_REAL") == "1" {
			t.Fatalf("no hardware adapter on the GPU lane: %s", adapter)
		}
		t.Skipf("a software adapter (%s)", adapter)
	}
	t.Logf("adapter: %s", adapter)
	for _, js := range []string{frameProbeJS, controlJS} {
		if _, err := page.Evaluate(js); err != nil {
			t.Fatal(err)
		}
	}

	f1 := func(v any) float64 { return num(v) }
	row := func(name string, secs int, w playwright.Worker) (pw, ph float64) {
		t.Helper()
		if _, err := page.Evaluate(`() => window.__frStart()`); err != nil {
			t.Fatal(err)
		}
		if w != nil {
			if _, err := w.Evaluate(`() => self.__wfStart()`); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(time.Duration(secs) * time.Second)
		v, err := page.Evaluate(`() => window.__frStop()`)
		if err != nil {
			t.Fatal(err)
		}
		m, _ := v.(map[string]any)
		sub := func(k, f string) float64 { mm, _ := m[k].(map[string]any); return f1(mm[f]) }
		line := fmt.Sprintf("FRAMERATE %-22s page %.1f animation frames/s (interval p95 %.0f ms, max %.0f ms); guest frames %.1f/s, acks %.1f/s; frame arrival to ack %.1f ms mean, %.0f p95, %.0f max",
			name, f1(m["raf"]), sub("rafInt", "p95"), sub("rafInt", "max"), f1(m["frames"]), f1(m["acks"]), sub("lat", "mean"), sub("lat", "p95"), sub("lat", "max"))
		if w != nil {
			wv, err := w.Evaluate(`() => self.__wfStop()`)
			if err != nil {
				t.Fatal(err)
			}
			wm, _ := wv.(map[string]any)
			ws := func(k, f string) float64 { mm, _ := wm[k].(map[string]any); return f1(mm[f]) }
			line += fmt.Sprintf("; worker: %.1f submits/s, submit to GPU completion %.1f ms mean, %.0f p95, %.0f max; transferToImageBitmap %.2f ms mean; frames posted %.1f/s",
				f1(wm["submits"]), ws("done", "mean"), ws("done", "p95"), ws("done", "max"), ws("xfer", "mean"), f1(wm["posted"]))
		}
		t.Log(line)
		if w != nil {
			sv, _ := w.Evaluate(`() => Object.entries(self.__wf.stacks)`)
			ents, _ := sv.([]any)
			for _, e := range ents {
				kv, _ := e.([]any)
				if len(kv) == 2 {
					t.Logf("FRAMERATE   submit site (%s): %.0f x %v", name, f1(kv[1]), kv[0])
				}
			}
		}
		return f1(m["w"]), f1(m["h"])
	}

	row("idle", 8, nil)
	// show moves between the Terminal and the Surfaces views and waits for it.
	show := func(view, keys string) {
		t.Helper()
		d.press("F6", keys)
		d.until("view "+view, 10*time.Second, func(p ui.Probe) bool { return p.View == view })
	}

	d.run("wctr run -d --gpu -m 512m --name q quake")
	d.until("quake running", 120*time.Second, func(p ui.Probe) bool {
		return len(p.Containers) == 1 && strings.Contains(p.Status[1], "1 running")
	})
	d.press("F6", "ArrowDown") // Terminal -> Surfaces
	d.until("a frame in the pane", 90*time.Second, func(p ui.Probe) bool {
		return p.View == "Surfaces" && len(p.SurfaceBoxes) == 1 && p.SurfaceBoxes[0].W > 0
	})
	var ew playwright.Worker
	for _, w := range page.Workers() {
		if strings.Contains(w.URL(), "engine-worker") {
			ew = w
		}
	}
	if ew == nil {
		t.Fatalf("no engine worker among %d workers", len(page.Workers()))
	}
	if _, err := ew.Evaluate(workerProbeJS); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second) // warm-up
	w, h := row("guest, pane shown", 12, ew)
	// The empty submits are the host library's kicks (kick.go: a Firefox-only
	// workaround, sent on every browser while a wait pends); drop them in the probe.
	if _, err := ew.Evaluate(`() => { self.__noKick = true; return true; }`); err != nil {
		t.Fatal(err)
	}
	row("guest, kicks dropped", 12, ew)
	if _, err := ew.Evaluate(`() => { self.__noKick = false; return true; }`); err != nil {
		t.Fatal(err)
	}
	show("Terminal", "ArrowUp") // the pane is hidden, the guest still runs
	time.Sleep(2 * time.Second)
	row("guest, pane hidden", 12, ew)
	d.run("wctr rm -f q")
	time.Sleep(3 * time.Second)
	if w < 16 || h < 16 {
		w, h = 640, 400
	}
	t.Logf("frame size %.0f x %.0f; controls at that size, the upload control at the viewport 1280 x 800", w, h)

	// GPU control: the same kind of work, no guest.
	if _, err := page.Evaluate(fmt.Sprintf(`() => window.__ctl.startGPU(%d, %d, 600, false)`, int(w), int(h))); err != nil {
		t.Fatal(err)
	}
	row("GPU control", 10, nil)
	if v, err := page.Evaluate(`() => window.__ctl.stopGPU()`); err == nil {
		m, _ := v.(map[string]any)
		t.Logf("FRAMERATE GPU control ran %.1f iterations/s (render + read-back + ImageBitmap, %.0f x %.0f)", f1(m["perSec"]), w, h)
	}
	// Upload control: the dashboard's full-canvas put, no guest.
	if _, err := page.Evaluate(`() => window.__ctl.startUpload(1280, 800)`); err != nil {
		t.Fatal(err)
	}
	row("upload control", 10, nil)
	// Both, the bitmaps drawn into the canvas as the dashboard does.
	if _, err := page.Evaluate(fmt.Sprintf(`() => window.__ctl.startGPU(%d, %d, 600, true)`, int(w), int(h))); err != nil {
		t.Fatal(err)
	}
	row("both controls", 10, nil)
	if v, err := page.Evaluate(`() => window.__ctl.stopGPU()`); err == nil {
		m, _ := v.(map[string]any)
		t.Logf("FRAMERATE both controls: the GPU loop ran %.1f iterations/s, %.0f bitmaps drawn", f1(m["perSec"]), f1(m["bitmaps"]))
	}
	_, _ = page.Evaluate(`() => window.__ctl.stopUpload()`)
}
