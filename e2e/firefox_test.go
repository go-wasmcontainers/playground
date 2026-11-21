//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"

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

// ffPage is wdPage with a keyboard that checks what it typed. Typing through
// geckodriver sometimes lands an extra character in the terminal ("-mo 512m",
// "-pm 512m" in 3 of about 60 runs: the line then fails as an invalid flag, exit
// 125), so Type reads the line back and types it again when it is not the text.
type ffPage struct{ wdPage }

func (p ffPage) Keyboard() playwright.Keyboard { return ffKeys{p, safariKeys{p.wdPage}} }

type ffKeys struct {
	p ffPage
	safariKeys
}

func (k ffKeys) Type(text string, o ...playwright.KeyboardTypeOptions) error {
	for attempt := 0; ; attempt++ {
		if err := k.safariKeys.Type(text, o...); err != nil {
			return err
		}
		// The engine echoes what is typed asynchronously: give the line a moment
		// to catch up before judging it.
		var v any
		var err error
		for i := 0; i < 50; i++ {
			v, err = k.p.Evaluate(`() => window.__playground.text().split("\n").pop()`)
			if line, _ := v.(string); err == nil && strings.HasSuffix(line, strings.TrimRight(text, " ")) {
				return nil
			}
			time.Sleep(20 * time.Millisecond)
		}
		if attempt == 3 {
			return fmt.Errorf("the terminal line is not %q after %d tries: %v", text, attempt+1, v)
		}
		// Rub the line out (more Backspaces than it holds) and type it again.
		backspaces := make([]string, len(text)+12)
		for i := range backspaces {
			backspaces[i] = "" // WebDriver's Backspace
		}
		if err := k.p.s.SendKeys(k.p.ctx, backspaces); err != nil {
			return err
		}
	}
}

// firefoxTab opens the playground's terminal page in Firefox, waits for the
// engine, and checks the adapter is a hardware one.
func firefoxTab(t *testing.T) (tab, string, func()) {
	t.Helper()
	return firefoxTabAt(t, "?ui=terminal")
}

// firefoxTabAt is firefoxTab on the page's URL with the given query.
func firefoxTabAt(t *testing.T, query string) (tab, string, func()) {
	t.Helper()
	page, logPath, stop := firefoxSession(t)
	b := tab{t, ffPage{page}, "firefox"}
	if err := page.s.Navigate(page.ctx, serve(t, true)+query); err != nil {
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

// deathLog is what a container that stopped said: the lines before and from
// the first sign of its death (a Go fatal error or panic, or the engine's trap
// line), not the tail, which is a dump of idle goroutines.
func deathLog(logs string) string {
	for _, sign := range []string{"fatal error:", "panic:", "wctr: the module trapped"} {
		if i := strings.Index(logs, sign); i >= 0 {
			from := max(0, i-1500)
			return "log around " + strconv.Quote(sign) + ":\n" + logs[from:min(len(logs), i+2500)]
		}
	}
	return "log tail:\n" + logs[len(logs)-min(len(logs), 3000):]
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
	quakeSteps(t, b, 25)
}

// TestQuakeSoakFirefox starts the quake image the way the reporter of #56 did,
// detached so the test can watch it, and checks it is still running after
// E2E_SOAK_SECONDS (default 60) in each of four modes: the main menu the image
// opens on, left alone, with and without --audio (it died at 20 to 30 s with "out
// of memory" until quake-wctr yielded each frame: the collector never ran), and
// the game, with the world on the CPU and on the GPU. A container that stops
// fails the test with the lines around its death.
func TestQuakeSoakFirefox(t *testing.T) {
	secs := 60
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
	for _, mode := range []struct{ name, args string }{ // "audio" in name: wctr run --audio
		{"menu", ""},
		{"menu-audio", ""},
		{"game", " --map e1m1"},
		{"gpuworld", " --map e1m1 --gpuworld"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			b := tab{t, b.page, b.who}
			b.run("wctr rm -f q") // a container a failed mode left behind
			runFlags := ""
			if strings.Contains(mode.name, "audio") {
				runFlags = " --audio"
			}
			b.must("wctr run -d --gpu"+runFlags+" -m 512m --name q quake"+mode.args, 0)
			t0 := time.Now()
			for time.Since(t0) < time.Duration(secs)*time.Second {
				time.Sleep(time.Second)
				if _, ps := b.run("wctr ps"); !strings.Contains(ps, "Up") {
					_, logs := b.run("wctr logs q")
					t.Fatalf("quake%s stopped after %.1f s: %s\n%s", mode.args, time.Since(t0).Seconds(), ps, deathLog(logs))
				}
			}
			t.Logf("quake%s: still running after %.0f s", mode.args, time.Since(t0).Seconds())
			b.must("wctr rm -f q", 0)
		})
	}
}
