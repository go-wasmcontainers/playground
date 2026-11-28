//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/mxschmitt/playwright-go"
)

// TestComposeInBrowser types `wctr compose config` into the page. The session
// passes composecli no file system, so the command falls back to os.OpenRoot,
// which a js/wasm page does not have. The command must reach compose's own
// "no file" answer, not an operating-system error about the working directory.
func TestComposeInBrowser(t *testing.T) {
	pw, err := playwright.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Stop()
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true), Timeout: playwright.Float(180000)})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer browser.Close()
	page, err := browser.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	b := tab{t, page, "chromium/compose"}
	if _, err := page.Goto(serve(t, false) + "?ui=terminal"); err != nil {
		t.Fatal(err)
	}
	b.ready()
	out := b.must("wctr compose config", 1)
	for _, bad := range []string{"not implemented", "ENOSYS", "function not implemented", "operation not supported", "getwd", "openroot"} {
		if strings.Contains(strings.ToLower(out), strings.ToLower(bad)) {
			t.Fatalf("compose fails on the platform (%q), not on the missing file:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "no Compose file found") {
		t.Fatalf("compose did not answer that the directory has no file:\n%s", out)
	}
}
