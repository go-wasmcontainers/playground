//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-wasmcontainers/playground/internal/webdriver"
)

// The Firefox lane (go-wasmcontainers/playground#56: quake exited 134 under
// Firefox). It runs the real Firefox through geckodriver (W3C WebDriver, the
// client safari_test.go already has), because Playwright's own Firefox build
// does not start on every runner. E2E_BROWSERS=firefox turns it on; GECKODRIVER
// names the driver (default: geckodriver on PATH) and FIREFOX the browser
// binary (default: the one geckodriver finds). Firefox runs headed: headless
// answers requestAdapter with null (go-wasmcontainers/gpu measured it). The lane
// FAILS, never skips, when Firefox cannot start or offers no hardware adapter.
//
// Manual check (a Firefox with WebGPU: 141 on Windows, 145 on macOS Tahoe with
// Apple silicon, 147 on older macOS with Apple silicon):
//
//	E2E_BROWSERS=firefox GECKODRIVER=/path/to/geckodriver \
//	  go test -tags e2e -count=1 -v -run 'TestQuakeFirefox|TestQuakeSoakFirefox' ./e2e/

// firefoxSession starts Firefox and returns its page, the file holding what
// Firefox and geckodriver printed (the page's console lines too:
// devtools.console.stdout.content), and a stop func.
func firefoxSession(t *testing.T) (wdPage, string, func()) {
	t.Helper()
	if !strings.Contains(os.Getenv("E2E_BROWSERS"), "firefox") {
		t.Skip("set E2E_BROWSERS=firefox (needs geckodriver and a Firefox with WebGPU)")
	}
	driver := os.Getenv("GECKODRIVER")
	if driver == "" {
		driver = "geckodriver"
	}
	ctx := context.Background()
	logPath := filepath.Join(t.TempDir(), "firefox.log")
	lf, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	c, stopDriver, err := webdriver.StartWith(ctx, driver, lf)
	if err != nil {
		lf.Close()
		t.Fatal(err)
	}
	opts := map[string]any{"prefs": map[string]any{
		"devtools.console.stdout.content": true,
		"dom.webgpu.enabled":              true,
	}}
	if bin := os.Getenv("FIREFOX"); bin != "" {
		opts["binary"] = bin
	}
	s, err := c.NewSession(ctx, map[string]any{"browserName": "firefox", "moz:firefoxOptions": opts})
	if err != nil {
		stopDriver()
		lf.Close()
		t.Fatalf("Firefox session: %v", err)
	}
	t.Logf("Firefox %s", s.Version)
	return wdPage{s, ctx}, logPath, func() { s.Delete(ctx); stopDriver(); lf.Close() }
}

// firefoxTab opens the playground's terminal page in Firefox, waits for the
// engine, and checks the adapter is a hardware one.
func firefoxTab(t *testing.T) (tab, string, func()) {
	t.Helper()
	page, logPath, stop := firefoxSession(t)
	b := tab{t, page, "firefox"}
	if err := page.s.Navigate(page.ctx, serve(t, true)+"?ui=terminal"); err != nil {
		stop()
		t.Fatal(err)
	}
	b.ready()
	// Firefox hides the vendor and the architecture; isFallbackAdapter and
	// the answer itself are what it tells.
	v := b.eval(`async () => {
	  if (!navigator.gpu) return "no navigator.gpu";
	  const a = await navigator.gpu.requestAdapter();
	  if (!a) return "requestAdapter returned null";
	  if (a.isFallbackAdapter || (a.info && a.info.isFallbackAdapter)) return "fallback adapter";
	  return "ok shader-f16=" + a.features.has("shader-f16");
	}`)
	t.Logf("adapter: %v", v)
	if s, _ := v.(string); !strings.HasPrefix(s, "ok") {
		stop()
		t.Fatalf("Firefox has no hardware WebGPU adapter: %v", v)
	}
	return b, logPath, stop
}

// fileTail is the last n bytes of the file.
func fileTail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	if len(data) > n {
		data = data[len(data)-n:]
	}
	return string(data)
}

// TestQuakeFirefox is TestQuake in Firefox: the view the guest draws has the
// reference's hash, the ImageBitmap the page receives has the RGBA the guest
// presented, and the game and the main menu keep drawing frames. It was red on
// gpu v0.14.0: the container exited 134 at its second frame, "Canvas not
// configured" (Firefox drops a canvas context's configuration when
// transferToImageBitmap takes the frame).
func TestQuakeFirefox(t *testing.T) {
	b, logPath, stop := firefoxTab(t)
	defer stop()
	defer func() {
		if t.Failed() {
			t.Logf("firefox output tail:\n%s", fileTail(logPath, 6000))
		}
	}()
	quakeSteps(t, b)
}

// TestQuakeSoakFirefox starts the quake image the way the reporter of #56 did,
// detached so the test can watch it, and checks it is still running after
// E2E_SOAK_SECONDS (default 45) in each of two modes: the main menu the image
// opens on, and the game with the world on the GPU. A container that stops
// fails the test with its logs, which now name a trap's cause.
func TestQuakeSoakFirefox(t *testing.T) {
	secs := 45
	if s := os.Getenv("E2E_SOAK_SECONDS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			t.Fatalf("E2E_SOAK_SECONDS=%q", s)
		}
		secs = n
	}
	b, logPath, stop := firefoxTab(t)
	defer stop()
	defer func() {
		if t.Failed() {
			t.Logf("firefox output tail:\n%s", fileTail(logPath, 6000))
		}
	}()
	for _, mode := range []struct{ name, args string }{
		{"menu", ""},
		{"gpuworld", " --map e1m1 --gpuworld"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			b := tab{t, b.page, b.who}
			b.must("wctr run -d --gpu -m 512m --name q quake"+mode.args, 0)
			t0 := time.Now()
			for time.Since(t0) < time.Duration(secs)*time.Second {
				time.Sleep(time.Second)
				if _, ps := b.run("wctr ps"); !strings.Contains(ps, "Up") {
					_, logs := b.run("wctr logs q")
					t.Fatalf("quake%s stopped after %.1f s: %s\nlogs tail:\n%s", mode.args, time.Since(t0).Seconds(), ps, logs[len(logs)-min(len(logs), 3000):])
				}
			}
			t.Logf("quake%s: still running after %.0f s", mode.args, time.Since(t0).Seconds())
			b.must("wctr rm -f q", 0)
		})
	}
}
