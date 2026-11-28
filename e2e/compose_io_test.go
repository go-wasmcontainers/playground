//go:build e2e

package e2e

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/go-wasmcontainers/dashboard/ui"
)

const (
	demoCompose  = "name: demo\nservices:\n  web:\n    image: hello\n"
	otherCompose = "name: other\nservices:\n  api:\n    image: hello\n"
)

// TestComposeImportExport drives the Topology view's compose controls as a
// person does: it opens Topology, clicks Import compose (a real file chooser),
// sees the `compose config` preview and the command that really ran, is asked
// before a second import replaces the first, and exports the imported project
// as a download. The page-wide mute is one state with the dashboard's.
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
	// The replace confirmation: answered by the test, and counted.
	var asked atomic.Int32
	var accept atomic.Bool
	page.On("dialog", func(dlg playwright.Dialog) {
		asked.Add(1)
		if accept.Load() {
			_ = dlg.Accept()
		} else {
			_ = dlg.Dismiss()
		}
	})
	d := dash{t, page, "chromium/compose-io", &log}
	if _, err := page.Goto(serve(t, false)); err != nil {
		t.Fatal(err)
	}
	d.ready()

	d.view("Topology")
	if v, _ := page.Evaluate(`() => typeof window.__wctrCompose.pick + typeof window.__wctrCompose.export`); v != "functionfunction" {
		t.Fatalf("the page offers no compose bridge: %v", v)
	}
	d.until("the compose buttons", 10*time.Second, func(p ui.Probe) bool {
		_, imp := p.Boxes["topology.import"]
		return imp
	})

	// Hostile input is refused as a whole, and nothing is kept.
	refused, err := page.Evaluate(`() => window.__wctrComposeImport(["../x", "compose.yaml"], ["a", "name: demo\nservices:\n  web:\n    image: hello\n"])`)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := refused.(string); s == "" {
		t.Fatal("a file named ../x was accepted")
	}
	if v, _ := page.Evaluate(`() => window.__wctrComposeImport(["compose.yaml"], [42])`); v == "" {
		t.Error("a non-text file was accepted")
	}
	if p := d.state(); p.LastCommand != "" && strings.Contains(p.LastCommand, "compose") {
		t.Errorf("a refused import shows a command as run: %q", p.LastCommand)
	}

	// Import compose: the real button, the real file chooser.
	pick := func(name, text string) {
		t.Helper()
		fc, err := page.ExpectFileChooser(func() error { d.click("topology.import"); return nil })
		if err != nil {
			t.Fatalf("Import compose opened no file chooser: %v", err)
		}
		if err := fc.SetFiles([]playwright.InputFile{{Name: name, MimeType: "text/yaml", Buffer: []byte(text)}}); err != nil {
			t.Fatal(err)
		}
	}
	pick("compose.yaml", demoCompose)
	if n := asked.Load(); n != 0 {
		t.Errorf("a first import asked to replace nothing (%d dialogs)", n)
	}
	p := d.until("the command that ran and its preview", 60*time.Second, func(p ui.Probe) bool {
		return strings.Contains(p.LastCommand, "compose config") && strings.Contains(strings.Join(p.TopologyDetail, "\n"), "web")
	})
	t.Logf("last command %q, detail %q", p.LastCommand, p.TopologyDetail)

	// A second import over files already held asks first; "no" keeps them.
	accept.Store(false)
	pick("compose.yaml", otherCompose)
	until := time.Now().Add(10 * time.Second)
	for asked.Load() == 0 && time.Now().Before(until) {
		time.Sleep(50 * time.Millisecond)
	}
	if asked.Load() != 1 {
		t.Fatalf("a second import replaced the first without asking (%d dialogs)", asked.Load())
	}
	time.Sleep(time.Second)
	d.run("wctr compose config")
	d.until("the first project is still the one held", 30*time.Second, func(p ui.Probe) bool {
		return strings.Contains(p.Terminal, "name: demo") && !strings.Contains(p.Terminal, "name: other")
	})
	// "yes" replaces them.
	accept.Store(true)
	d.view("Topology")
	pick("compose.yaml", otherCompose)
	d.until("the replacing import", 60*time.Second, func(p ui.Probe) bool {
		return strings.Contains(strings.Join(p.TopologyDetail, "\n"), "api")
	})

	// Export: a name that is not a project, and a project that is not the
	// imported one, are refused; the imported one downloads.
	for _, name := range []string{"a b; rm", "demo"} {
		bad, err := page.Evaluate(`async (n) => { try { await window.__wctrComposeExport(n); return "accepted" } catch (e) { return String(e) } }`, name)
		if err != nil {
			t.Fatal(err)
		}
		if s, _ := bad.(string); !strings.HasPrefix(s, "export:") {
			t.Errorf("export of %q: %q", name, s)
		}
	}
	dl, err := page.ExpectDownload(func() error {
		_, err := page.Evaluate(`() => window.__wctrCompose.export("other")`)
		return err
	})
	if err != nil {
		t.Fatalf("export: %v\nconsole: %s", err, strings.Join(log, "\n"))
	}
	if got := dl.SuggestedFilename(); got != "other.compose.yaml" {
		t.Errorf("download named %q, want other.compose.yaml", got)
	}

	// With nothing selected the Export button refuses, and offers no download.
	d.view("Topology")
	if _, err := page.ExpectDownload(func() error { d.click("topology.export"); return nil }, playwright.PageExpectDownloadOptions{Timeout: playwright.Float(1500)}); err == nil {
		t.Error("Export compose with no project selected downloaded a file")
	}

	// The page-wide mute is one state: the Topology button, the dashboard's
	// Sound.All and the page's gain agree, whichever side moves it.
	muted := func() bool { v, _ := page.Evaluate(`() => window.__wctrAudio ? window.__wctrAudio.pageMuted() : null`); b, _ := v.(bool); return b }
	waitMuted := func(want bool, what string) {
		t.Helper()
		end := time.Now().Add(5 * time.Second)
		for muted() != want && time.Now().Before(end) {
			time.Sleep(50 * time.Millisecond)
		}
		if muted() != want {
			t.Fatalf("%s: the page's mute is %v, want %v", what, muted(), want)
		}
	}
	if v, _ := page.Evaluate(`() => !!window.__wctrAudio`); v != true {
		t.Skip("no audio on this page")
	}
	d.click("topology.muteall")
	waitMuted(true, "Mute all in Topology")
	// The page strip's button calls window.__wctrSoundAll: the dashboard's state
	// follows, so the next click of Mute all unmutes instead of muting again.
	if _, err := page.Evaluate(`() => window.__wctrSoundAll(false)`); err != nil {
		t.Fatal(err)
	}
	waitMuted(false, "the strip's unmute")
	d.click("topology.muteall")
	waitMuted(true, "Mute all after the strip unmuted (the two had split)")
	d.click("topology.muteall")
	waitMuted(false, "Unmute all in Topology")
}
