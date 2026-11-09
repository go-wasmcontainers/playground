//go:build e2e

package e2e

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/mxschmitt/playwright-go"
)

// Goldens of the quake image's --worldref 4 (the eye at info_player_start
// of the freepak v0.2.0 e1m1, 640x400): the figures go-quake1/engine's
// quake-worldref prints for the same view (the 8-bit index frame) and
// webgpupresent.Reference computes (the RGBA frame the page must show).
//
// They are the figures of a build WITHOUT fused multiply-add, which is what
// WebAssembly and amd64 compute. A native arm64 build fuses x*y+z and
// draws 1a6f7e81...406c / 34bb6b5b...3ee6 for the same view; the same
// native build with `-gcflags=all=-d=fmahash=n` (fusion off) prints these.
const (
	quakeIndicesGolden = "580802e29ba5af8f8891fcaf7b8e89d76fc6febfa9fcee4165e04e504d6dd2cc"
	quakeRGBAGolden    = "f84421f86941b51dbb5afafd0eb2cb6a608314e3064b07100ff83c2fb2b3c8df"
)

// TestQuake runs the quake image (go-quake1's engine as a wasip1 guest,
// software renderer, frames shown through wasi-gfx:surface) on a real GPU
// (E2E_GPU=1, as TestGPU): the view the guest draws has the hash the
// reference tool prints, the ImageBitmap the page receives has exactly the
// RGBA the guest says it presented, and when the game runs its frames keep
// changing.
func TestQuake(t *testing.T) {
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
	b := tab{t, page, "chromium/quake"}
	if _, err := page.Goto(serve(t, true) + "?ui=terminal"); err != nil {
		t.Fatal(err)
	}
	b.ready()

	info, _ := b.eval(`async () => {
	  if (!navigator.gpu) return "no navigator.gpu";
	  const a = await navigator.gpu.requestAdapter();
	  if (!a) return "no adapter";
	  const i = a.info || {};
	  const fb = i.isFallbackAdapter || a.isFallbackAdapter;
	  return [i.vendor, i.architecture, i.device, i.description, fb ? "fallback" : ""].join("|");
	}`).(string)
	t.Logf("adapter: %s", info)
	if strings.Contains(strings.ToLower(info), "swiftshader") || strings.Contains(info, "fallback") || strings.HasPrefix(info, "no ") {
		if os.Getenv("E2E_GPU_REAL") == "1" {
			t.Fatalf("a software adapter on the GPU lane: %s", info)
		}
		t.Skipf("no real GPU adapter (%s): the engine refuses software adapters, so there is no frame to check", info)
	}

	// Every frame the page receives: its size and a hash of its RGBA, and
	// the last one's pixels, read back through a 2D canvas.
	b.eval(`() => {
	  window.__q = { hashes: [], last: null, w: 0, h: 0 };
	  window.__wctrFrameProbe = (m) => {
	    const w = m.bitmap.width, h = m.bitmap.height;
	    const c = new OffscreenCanvas(w, h), g = c.getContext("2d", { willReadFrequently: true });
	    g.drawImage(m.bitmap, 0, 0);
	    const d = g.getImageData(0, 0, w, h).data;
	    let x = 2166136261;
	    for (let i = 0; i < d.length; i++) { x = Math.imul(x ^ d[i], 16777619) >>> 0; }
	    window.__q.hashes.push(x);
	    window.__q.last = d; window.__q.w = w; window.__q.h = h;
	  };
	}`)

	// 1. --worldref 4: the guest's hash is the golden, and the page shows
	// the guest's RGBA.
	out := b.must("wctr run --rm --gpu -m 512m quake --worldref 4", 0, "view 4 640x400 sha256(indices) ")
	re := func(kind string) string {
		m := regexp.MustCompile(`view 4 640x400 sha256\(` + kind + `\) ([0-9a-f]{64})`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("no %s hash in:\n%s", kind, out)
		}
		return m[1]
	}
	if got := re("indices"); got != quakeIndicesGolden {
		t.Errorf("guest indices hash %s, golden %s", got, quakeIndicesGolden)
	}
	guestRGBA := re("rgba")
	if guestRGBA != quakeRGBAGolden {
		t.Errorf("guest RGBA hash %s, golden %s", guestRGBA, quakeRGBAGolden)
	}
	b.until("no frame reached the page", `() => window.__q.hashes.length > 0`, 30e9)
	page640 := b.eval(`async () => {
	  const h = await crypto.subtle.digest("SHA-256", window.__q.last);
	  return window.__q.w + "x" + window.__q.h + " " + [...new Uint8Array(h)].map((v) => v.toString(16).padStart(2, "0")).join("");
	}`).(string)
	t.Logf("presented frame: %s", page640)
	if want := "640x400 " + guestRGBA; page640 != want {
		t.Errorf("the page's ImageBitmap is %s, the guest presented %s", page640, want)
	}

	// 2. Play: 150 frames, no --worldref. They keep changing.
	b.eval(`() => { window.__q.hashes = []; }`)
	out = b.must("wctr run --rm --gpu -m 512m quake --frames 150", 0, "150 frames in ")
	t.Log(strings.TrimSpace(out[strings.LastIndex(out, "150 frames in"):]))
	n := int(num(b.eval(`() => window.__q.hashes.length`)))
	distinct := int(num(b.eval(`() => new Set(window.__q.hashes).size`)))
	t.Logf("quake: %d frames reached the page, %d distinct", n, distinct)
	if n < 120 || distinct < 2 {
		t.Fatalf("quake: %d frames, %d distinct; want at least 120 that change", n, distinct)
	}
}
