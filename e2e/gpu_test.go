//go:build e2e

package e2e

import (
	"os"
	"strings"
	"testing"

	"github.com/mxschmitt/playwright-go"
)

// TestGPU runs the GPU images with a GPU (E2E_GPU=1): compute matches the
// CPU, triangle presents frames that change, and without --gpu the same
// images get no adapter. It needs a browser with WebGPU:
//
//   - a GPU lane (macOS): E2E_GPU_REAL=1 also refuses a SwiftShader
//     adapter, so the work is known to run on the GPU;
//   - Linux CI: SwiftShader on Vulkan, E2E_GPU_ARGS="--enable-features=Vulkan
//     --use-vulkan=swiftshader --use-angle=swiftshader" (the only flags with
//     which headless Chromium presents a WebGPU canvas; gpu docs/DESIGN.md).
//
// Chromium runs with channel "chromium" (the new headless mode, a full
// browser: the old headless shell has no WebGPU).
func TestGPU(t *testing.T) {
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
	page, err := browser.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	b := tab{t, page, "chromium/gpu"}
	if _, err := page.Goto(serve(t, true) + "?ui=terminal"); err != nil {
		t.Fatal(err)
	}
	b.ready()

	info, _ := b.eval(`async () => {
	  if (!navigator.gpu) return "no navigator.gpu";
	  const a = await navigator.gpu.requestAdapter();
	  if (!a) return "no adapter";
	  const i = a.info || {};
	  return [i.vendor, i.architecture, i.device, i.description, a.isFallbackAdapter ? "fallback" : ""].join("|");
	}`).(string)
	t.Logf("adapter: %s", info)
	if strings.HasPrefix(info, "no ") {
		t.Fatalf("no WebGPU in this browser: %s", info)
	}
	if os.Getenv("E2E_GPU_REAL") == "1" && (strings.Contains(strings.ToLower(info), "swiftshader") || strings.Contains(info, "fallback")) {
		t.Fatalf("a software adapter on the GPU lane: %s", info)
	}

	// Each frame, scaled to 16x16, hashed: the frames must change.
	b.eval(`() => {
	  window.__frameHashes = [];
	  window.__wctrFrameProbe = (m) => {
	    const c = new OffscreenCanvas(16, 16), g = c.getContext("2d");
	    g.drawImage(m.bitmap, 0, 0, 16, 16);
	    const d = g.getImageData(0, 0, 16, 16).data;
	    let h = 0, lit = 0;
	    for (let i = 0; i < d.length; i++) { h = (h * 31 + d[i]) | 0; if (i % 4 !== 3 && d[i] > 32) lit++; }
	    window.__frameHashes.push(lit > 0 ? h : "dark");
	  };
	}`)

	b.must("wctr run --rm compute", 2, "no GPU adapter")
	out := b.must("wctr run --rm --gpu compute", 0, "match the CPU")
	if strings.Count(out, "match the CPU") != 2 {
		t.Fatalf("compute:\n%s", out)
	}
	b.must("wctr run --rm --gpu triangle 60", 0, "60 frames in")
	n := int(num(b.eval(`() => window.__frameHashes.length`)))
	distinct := int(num(b.eval(`() => new Set(window.__frameHashes.filter((h) => h !== "dark")).size`)))
	t.Logf("triangle: %d frames reached the page, %d distinct", n, distinct)
	if n < 60 || distinct < 2 {
		t.Fatalf("triangle: %d frames, %d distinct lit ones; want 60 that change", n, distinct)
	}
	b.must("wctr run --rm --gpu mnist gpu", 0, "digit 7")
}
