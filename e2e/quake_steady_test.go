//go:build e2e

package e2e

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/mxschmitt/playwright-go"
)

// TestQuakeSteadyState plays the quake image's main menu for 3600 frames (60 s)
// with --memstats on a real GPU (E2E_GPU=1, as TestQuake) and checks that nothing
// drifts: in every window of 600 frames after the first, the frame rate is
// 60 fps within 5 % (57 to 63), the guest allocates at most 64 KiB per frame
// (it was 331 KiB: 250 KiB of it a copy of the index frame per host call, go-wasmcontainers/gpu's
// cabi, 7 KiB since), and the heap in use (garbage included: at 7 KiB per frame
// it climbs 4.5 MB per window until a collection) does not grow by more than
// 64 MiB from the first window to the last. It was written for a
// report that the frame rate fell from 60 to 52 over 70 s; on the terminal page
// it measured 60.0 fps throughout, before and after the allocation fix.
func TestQuakeSteadyState(t *testing.T) {
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
	if _, err := page.Goto(serve(t, true) + "?ui=terminal"); err != nil {
		t.Fatal(err)
	}
	b := tab{t, page, "chromium/steady"}
	b.ready()
	if v, _ := b.eval(`async () => { const a = navigator.gpu && await navigator.gpu.requestAdapter(); return !a ? "none" : (a.info && a.info.isFallbackAdapter) || a.isFallbackAdapter ? "fallback" : "ok"; }`).(string); v != "ok" {
		if os.Getenv("E2E_GPU_REAL") == "1" {
			t.Fatalf("no hardware adapter: %s", v)
		}
		t.Skipf("no hardware adapter (%s)", v)
	}
	_, out := b.run("wctr run --rm --gpu -m 512m quake --memstats --frames 3600")
	if strings.Contains(out, "out of memory") {
		t.Fatalf("out of memory:\n%s", out[len(out)-min(len(out), 1500):])
	}
	re := regexp.MustCompile(`memstats frame (\d+) at (\d+) ms: alloc (\d+) KiB, inuse \d+ KiB, sys \d+ KiB, gcs (\d+), total alloc (\d+) KiB, mallocs \d+`)
	type sample struct{ frame, ms, live, gcs, total int }
	var s []sample
	for _, m := range re.FindAllStringSubmatch(out, -1) {
		v := make([]int, 5)
		for i, x := range []string{m[1], m[2], m[3], m[4], m[5]} {
			v[i], _ = strconv.Atoi(x)
		}
		s = append(s, sample{v[0], v[1], v[2], v[3], v[4]})
	}
	if len(s) < 50 {
		t.Fatalf("%d memstats lines in the output of 3600 frames (every 60), want at least 50:\n%s", len(s), out[len(out)-min(len(out), 1500):])
	}
	for i := 10; i < len(s); i += 10 {
		a, z := s[i-10], s[i]
		fps := float64(z.frame-a.frame) * 1000 / float64(z.ms-a.ms)
		perFrame := (z.total - a.total) / (z.frame - a.frame)
		t.Logf("frames %4d to %4d: %.1f fps, %d KiB allocated per frame, live heap %d KiB, %d collections", a.frame, z.frame, fps, perFrame, z.live, z.gcs)
		if i == 10 {
			continue // the first window includes the start-up
		}
		if fps < 57 || fps > 63 {
			t.Errorf("frames %d to %d ran at %.1f fps, want 57 to 63", a.frame, z.frame, fps)
		}
		if perFrame > 64 {
			t.Errorf("frames %d to %d allocated %d KiB per frame, want at most 64", a.frame, z.frame, perFrame)
		}
	}
	if first, last := s[10].live, s[len(s)-1].live; last > first+64<<10 {
		t.Errorf("the live heap grew from %d to %d KiB", first, last)
	}
}
