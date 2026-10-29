//go:build e2e

package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/go-wasmcontainers/dashboard/ui"
)

// TestClassDashboard joins a class from the dashboard (the default page),
// through window.__wctrClass and the dashboard's own dialog: the relay is
// set (useRelay, then the reload), the name and code are typed into the
// canvas one key at a time, a container on the class network is reached
// from a native session, and the code is nowhere the page keeps things.
func TestClassDashboard(t *testing.T) {
	if os.Getenv("E2E_URL") != "" {
		t.Skip("E2E_URL set: TestLive only")
	}
	pw, err := playwright.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Stop()
	for _, c := range []struct{ browser, mode string }{{"chromium", "service-worker"}, {"chromium", "headers"}, {"firefox", "headers"}} {
		if os.Getenv("E2E_BROWSERS") != "" && !strings.Contains(os.Getenv("E2E_BROWSERS"), c.browser) {
			continue
		}
		t.Run(c.browser+"/"+c.mode, func(t *testing.T) { classDashboard(t, pw, c.browser, c.mode) })
	}
}

func classDashboard(t *testing.T, pw *playwright.Playwright, browserName, mode string) {
	url, _ := serveCopy(t, mode == "headers")
	pub, r, class := startRelay(t, originOf(url))
	bt := pw.Chromium
	if browserName == "firefox" {
		bt = pw.Firefox
	}
	browser, err := bt.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true), Timeout: playwright.Float(180000)})
	if err != nil {
		t.Fatalf("launch %s: %v", browserName, err)
	}
	defer browser.Close()
	ctx, err := browser.NewContext(playwright.BrowserNewContextOptions{Viewport: &playwright.Size{Width: 1280, Height: 800}, IgnoreHttpsErrors: playwright.Bool(true)})
	if err != nil {
		t.Fatal(err)
	}
	page, err := ctx.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	var log []string
	page.On("console", func(m playwright.ConsoleMessage) { log = append(log, m.Text()) })
	d := dash{t, page, browserName + "/" + mode + " (dashboard class)", &log}
	if _, err := page.Goto(url); err != nil {
		t.Fatal(err)
	}
	d.ready()

	// The page tells the dashboard it can join a class; the relay is set
	// through it (the page reloads, the engine worker gets that origin).
	if v, _ := page.Evaluate(`() => typeof window.__wctrClass === "object" && window.__wctrClass.relay === ""`); v != true {
		t.Fatalf("%s: no __wctrClass, or a relay already set", d.who)
	}
	page.Evaluate(`(o) => { setTimeout(() => window.__wctrClass.useRelay(o), 0); }`, pub.URL)
	deadline := time.Now().Add(60 * time.Second)
	for {
		v, _ := page.Evaluate(`() => !!(window.__wctrClass && window.__wctrClass.relay)`)
		if v == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the relay was not taken", d.who)
		}
		time.Sleep(100 * time.Millisecond)
	}
	d.ready()

	// The dashboard's dialog: Networks view (Alt+4), Join class, name, code.
	d.press("Alt+4")
	d.until("the class button", 10*time.Second, func(p ui.Probe) bool { return p.View == "Networks" && p.Boxes["class.button"].W > 0 })
	d.click("class.button")
	d.until("the dialog", 5*time.Second, func(p ui.Probe) bool { return p.ClassOpen && p.Focus == "class.name" })
	d.typeKeys("ada")
	d.click("class.code")
	d.typeKeys(class.JoinCode)
	d.until("code typed", 5*time.Second, func(p ui.Probe) bool { return p.CodeLength == len(class.JoinCode) })
	d.press("Enter")
	deadline = time.Now().Add(30 * time.Second)
	var label string
	for {
		v, _ := page.Evaluate(`() => JSON.stringify(window.__wctrClass.status())`)
		s, _ := v.(string)
		if strings.Contains(s, `"state":"joined"`) {
			l, _ := page.Evaluate(`() => window.__wctrClass.status().label`)
			label, _ = l.(string)
			break
		}
		if time.Now().After(deadline) || strings.Contains(s, `"state":"error"`) {
			t.Fatalf("%s: not joined: %s\nconsole: %q", d.who, s, log)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The code is nowhere the page keeps things; the name (not secret) is,
	// so the check reads what it should.
	where, err := page.Evaluate(`() => [location.href, JSON.stringify(localStorage), JSON.stringify(sessionStorage),
		document.documentElement.outerHTML, window.__dashboard.state(), JSON.stringify(window.__wctrClass.status())].join("\n")`)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := where.(string)
	if !strings.Contains(s, "ada") {
		t.Fatalf("%s: the leak check read nothing useful", d.who)
	}
	if strings.Contains(s, class.JoinCode) {
		t.Fatalf("%s: the join code is readable in the page", d.who)
	}

	// A web server on the class network, reached from a native session.
	d.run("wctr run -d --name web --network class net serve")
	bob := nativeSession(t, r, class.JoinCode, "bob")
	defer bob.Close()
	ep, err := bob.Attach(context.Background(), "probe")
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for deadline := time.Now().Add(30 * time.Second); ; {
		if body, err = getThrough(ep, "web."+label); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil || !strings.Contains(body, "hello bob from 10.") {
		t.Fatalf("%s: GET web.%s: %q %v", d.who, label, body, err)
	}
}

// typeKeys presses one key per character (a canvas listens to keydown).
func (d dash) typeKeys(s string) {
	d.t.Helper()
	for _, c := range s {
		k := string(c)
		if c == ' ' {
			k = "Space"
		}
		d.press(k)
	}
}
