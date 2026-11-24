//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// slowTerminal makes xterm.js parse what it is given 400 ms late, in order, as
// it does when a backlog of output is queued (its write buffer is drained in
// time slices, so the screen buffer lags what was written).
const slowTerminal = `(() => {
  let T;
  Object.defineProperty(window, "Terminal", {
    configurable: true,
    get() { return T; },
    set(v) {
      T = class extends v {
        write(data, cb) { setTimeout(() => super.write(data, cb), 400); }
      };
    },
  });
})();`

// TestTranscriptEntryMeansTheOutputIsOnScreen: window.__playground.state.transcript
// gets an entry when a command is done, and the tests (and the e2e lanes that
// read window.__playground.text() the moment it grows) take the screen to hold
// the command's whole output by then. It did not: the engine's output goes
// through term.write, which parses later, so a read at that moment could see
// part of it ("wctr logs y": output lacks "bye": w, engine#84's CI).
func TestTranscriptEntryMeansTheOutputIsOnScreen(t *testing.T) {
	pw, err := playwright.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Stop()
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true), Timeout: playwright.Float(180000)})
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	page, err := browser.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	if err := page.AddInitScript(playwright.Script{Content: playwright.String(slowTerminal)}); err != nil {
		t.Fatal(err)
	}
	if _, err := page.Goto(serve(t, true) + "?ui=terminal"); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	for {
		v, err := page.Evaluate(`() => !!(window.__playground && window.__playground.state.ready)`)
		if err == nil && v == true {
			break
		}
		if time.Since(t0) > 90*time.Second {
			t.Fatal("engine not ready after 90s")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := page.Keyboard().Type("wctr run --rm hello"); err != nil {
		t.Fatal(err)
	}
	if err := page.Keyboard().Press("Enter"); err != nil {
		t.Fatal(err)
	}
	for {
		v, err := page.Evaluate(`() => window.__playground.state.transcript.length > 0`)
		if err == nil && v == true {
			break
		}
		if time.Since(t0) > 150*time.Second {
			t.Fatal("the command did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	txt, err := page.Evaluate(`() => window.__playground.text()`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(txt.(string), "Hello from a WebAssembly container!") {
		t.Fatalf("the transcript has the command, the screen lacks its output:\n%s", txt)
	}
}
