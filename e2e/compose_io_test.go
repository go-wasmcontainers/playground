//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/go-wasmcontainers/dashboard/ui"
)

// TestComposeImportExport drives the Topology view's compose controls through
// the page's side (web/compose-io.js and the dashboard's bridge): files handed
// to window.__wctrComposeImport land in the session's one compose FS (the one
// `wctr compose` reads), a hostile name is refused before any byte is kept,
// and window.__wctrCompose.export saves the resolved project as a download.
func TestComposeImportExport(t *testing.T) {
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
	ctx, err := browser.NewContext(playwright.BrowserNewContextOptions{AcceptDownloads: playwright.Bool(true), Viewport: &playwright.Size{Width: 1280, Height: 800}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := ctx.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	var log []string
	page.On("pageerror", func(err error) { log = append(log, "pageerror: "+err.Error()) })
	d := dash{t, page, "chromium/compose-io", &log}
	if _, err := page.Goto(serve(t, false)); err != nil {
		t.Fatal(err)
	}
	d.ready()

	d.until("the Topology view", 10*time.Second, func(p ui.Probe) bool { return p.View != "" })
	if v, _ := page.Evaluate(`() => typeof window.__wctrCompose.pick + typeof window.__wctrCompose.export`); v != "functionfunction" {
		t.Fatalf("the page offers no compose bridge: %v", v)
	}

	// A hostile name is refused as a whole: nothing is kept.
	refused, err := page.Evaluate(`() => window.__wctrComposeImport(["../x", "compose.yaml"], ["a", "name: demo\nservices:\n  web:\n    image: hello\n"])`)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := refused.(string); s == "" {
		t.Fatal("a file named ../x was accepted")
	}
	// Not a text file: refused by the bridge.
	if v, _ := page.Evaluate(`() => window.__wctrComposeImport(["compose.yaml"], [42])`); v == "" {
		t.Error("a non-text file was accepted")
	}

	ok, err := page.Evaluate(`() => window.__wctrComposeImport(["compose.yaml"], ["name: demo\nservices:\n  web:\n    image: hello\n"])`)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := ok.(string); s != "" {
		t.Fatalf("a valid compose file was refused: %s", s)
	}

	// The Terminal's `wctr compose config` reads the same FS.
	d.run("wctr compose config")
	d.until("the imported project in the terminal", 30*time.Second, func(p ui.Probe) bool {
		return strings.Contains(p.Terminal, "demo")
	})

	// Export: a project name that is not one is refused; a real one downloads.
	bad, err := page.Evaluate(`async () => { try { await window.__wctrComposeExport("a b; rm"); return "accepted" } catch (e) { return String(e) } }`)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := bad.(string); !strings.HasPrefix(s, "export:") {
		t.Errorf("a hostile project name: %q", s)
	}
	dl, err := page.ExpectDownload(func() error {
		_, err := page.Evaluate(`() => window.__wctrCompose.export("demo")`)
		return err
	})
	if err != nil {
		t.Fatalf("export: %v\nconsole: %s", err, strings.Join(log, "\n"))
	}
	if got := dl.SuggestedFilename(); got != "demo.compose.yaml" {
		t.Errorf("download named %q, want demo.compose.yaml", got)
	}
}
