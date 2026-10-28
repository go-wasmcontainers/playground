//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"

	"github.com/go-wasmcontainers/dashboard/ui"
)

// The dashboard (the playground's default page) driven as a person does:
// real key presses (one per character: a canvas listens to keydown, which
// Playwright's Type does not send) and real clicks, at the coordinates the
// dashboard reports for its controls; the state is read back through
// window.__dashboard.state(), never from pixels.

type dash struct {
	t    *testing.T
	page playwright.Page
	who  string
	log  *[]string
}

func (d dash) state() ui.Probe {
	d.t.Helper()
	p := ui.Probe{Status: make([]string, 4)}
	v, err := d.page.Evaluate("() => window.__dashboard ? window.__dashboard.state() : ''")
	if err != nil {
		*d.log = append(*d.log, "state: "+err.Error()) // the isolating reload
		return p
	}
	if s, _ := v.(string); s != "" {
		if err := json.Unmarshal([]byte(s), &p); err != nil {
			d.t.Fatal(err)
		}
	}
	return p
}

func (d dash) until(what string, timeout time.Duration, cond func(ui.Probe) bool) ui.Probe {
	d.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		p := d.state()
		if cond(p) {
			return p
		}
		if time.Now().After(deadline) {
			st, _ := d.page.Locator("#status").TextContent()
			d.t.Fatalf("%s: timed out waiting for %s\nstate: %+v\npage status: %q\nconsole: %s", d.who, what, p, st, strings.Join(*d.log, "\n"))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ready waits for the dashboard to show the engine's version, and returns
// how long that took.
func (d dash) ready() time.Duration {
	d.t.Helper()
	t0 := time.Now()
	d.until("the dashboard", 90*time.Second, func(p ui.Probe) bool {
		return strings.HasPrefix(p.Status[0], "engine ") && !strings.HasPrefix(p.Status[0], "engine: ")
	})
	return time.Since(t0)
}

func (d dash) click(name string) {
	d.t.Helper()
	b, ok := d.state().Boxes[name]
	if !ok {
		d.t.Fatalf("%s: no %s on screen", d.who, name)
	}
	// Boxes are in the canvas's backing-store pixels (dashboard v0.4.1:
	// CSS px × devicePixelRatio); the mouse moves in CSS px.
	dpr := 1.0
	if v, err := d.page.Evaluate(`() => window.devicePixelRatio`); err == nil {
		switch x := v.(type) {
		case float64:
			dpr = x
		case int:
			dpr = float64(x)
		}
	}
	if err := d.page.Mouse().Click(float64(b.X+b.W/2)/dpr, float64(b.Y+b.H/2)/dpr); err != nil {
		d.t.Fatal(err)
	}
}

func (d dash) press(keys ...string) {
	d.t.Helper()
	for _, k := range keys {
		if err := d.page.Keyboard().Press(k); err != nil {
			d.t.Fatal(err)
		}
	}
}

// view moves the navigation to a view: F6 then arrows (the views are
// Containers, Images, Volumes, Networks, Terminal, in that order).
func (d dash) view(name string) {
	d.t.Helper()
	order := []string{"Containers", "Images", "Volumes", "Networks", "Terminal"}
	from := slices.Index(order, d.state().View)
	to := slices.Index(order, name)
	if from < 0 || to < 0 {
		d.t.Fatalf("%s: view %q from %q", d.who, name, d.state().View)
	}
	d.press("F6")
	for ; from < to; from++ {
		d.press("ArrowDown")
	}
	for ; from > to; from-- {
		d.press("ArrowUp")
	}
	d.until("view "+name, 5*time.Second, func(p ui.Probe) bool { return p.View == name })
}

// run types a line into the dashboard's terminal.
func (d dash) run(line string) {
	d.t.Helper()
	d.view("Terminal")
	d.click("terminal")
	for _, c := range line {
		k := string(c)
		if c == ' ' {
			k = "Space"
		}
		d.press(k)
	}
	d.press("Enter")
}

// DashReport is what a dashboard run measured.
type DashReport struct {
	Browser string `json:"browser"`
	Mode    string `json:"mode"`
	// WallReadyMs: navigation to the engine's version on the canvas (the
	// xterm.js page's EngineReadyMs measures the same span to its prompt).
	WallReadyMs float64 `json:"wallReadyMs"`
	// In the page, from app.js start: the engine worker's "ready", and
	// dashboard.wasm running.
	EngineReadyMs    float64 `json:"engineReadyMs"`
	DashboardReadyMs float64 `json:"dashboardReadyMs"`
	WarmReadyMs      float64 `json:"warmReadyMs"`
	OfflineReadyMs   float64 `json:"offlineReadyMs,omitempty"`
}

func dashboard(t *testing.T, pw *playwright.Playwright, browserName, mode string) DashReport {
	url := os.Getenv("E2E_URL")
	if mode != "live" {
		url, _ = serveCopy(t, strings.HasPrefix(mode, "headers"))
	}
	// headers-dpr2: a HiDPI screen (the canvas's backing store is CSS px ×
	// devicePixelRatio; clicks at the reported boxes must still land).
	scale := 1.0
	if strings.HasSuffix(mode, "-dpr2") {
		scale = 2
	}
	bt := pw.Chromium
	if browserName == "firefox" {
		bt = pw.Firefox
	}
	browser, err := bt.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true), Timeout: playwright.Float(180000)})
	if err != nil {
		t.Fatalf("launch %s: %v", browserName, err)
	}
	defer browser.Close()
	ctx, err := browser.NewContext(playwright.BrowserNewContextOptions{Viewport: &playwright.Size{Width: 1280, Height: 800}, DeviceScaleFactor: playwright.Float(scale)})
	if err != nil {
		t.Fatal(err)
	}
	page, err := ctx.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	var log []string
	page.On("console", func(m playwright.ConsoleMessage) { log = append(log, m.Text()) })
	page.On("pageerror", func(err error) { log = append(log, "pageerror: "+err.Error()) })
	d := dash{t, page, browserName + "/" + mode + " (dashboard)", &log}
	rep := DashReport{Browser: browserName, Mode: mode}
	inPage := func() {
		for i := 0; i < 20 && rep.DashboardReadyMs <= 0; i++ {
			time.Sleep(25 * time.Millisecond) // app.js samples every 20 ms
			v, err := page.Evaluate(`() => [window.__playground.state.engineReadyMs, window.__playground.state.dashboardReadyMs]`)
			if err != nil {
				t.Fatal(err)
			}
			if a, _ := v.([]any); len(a) == 2 {
				rep.EngineReadyMs, rep.DashboardReadyMs = num(a[0]), num(a[1])
			}
		}
	}

	t0 := time.Now()
	if _, err := page.Goto(url); err != nil {
		t.Fatal(err)
	}
	d.ready()
	rep.WallReadyMs = float64(time.Since(t0).Milliseconds())
	inPage()
	if iso, _ := page.Evaluate(`() => crossOriginIsolated`); iso != true {
		t.Fatalf("%s: page not cross-origin isolated", d.who)
	}

	// The terminal runs wctr (pulling from the catalogue); the container
	// appears in the dashboard.
	d.run("wctr run -d --name web lab count 100000 100")
	d.until("web running", 90*time.Second, func(p ui.Probe) bool {
		return slices.Contains(p.Containers, "web") && strings.Contains(p.Status[1], "1 running")
	})
	// Select it, read its logs, stop it with the button.
	d.view("Containers")
	d.press("F6", "ArrowDown")
	d.until("web selected", 5*time.Second, func(p ui.Probe) bool { return p.Selected != "" })
	d.until("logs", 30*time.Second, func(p ui.Probe) bool { return strings.Contains(p.Logs, "3") })
	d.click("containers.stop")
	p := d.until("stopped", 30*time.Second, func(p ui.Probe) bool { return strings.Contains(p.Status[1], "0 running") && p.Pending == 0 })
	if p.LastCommand != "wctr stop web" {
		t.Errorf("%s: last command = %q", d.who, p.LastCommand)
	}
	if !slices.Contains(p.Images, "lab") && !slices.ContainsFunc(p.Images, func(s string) bool { return strings.HasPrefix(s, "lab") }) {
		t.Errorf("%s: images %q, want lab", d.who, p.Images)
	}
	// A foreground run: its output in the dashboard's terminal.
	d.run("wctr run --rm lab echo from-the-dashboard")
	d.until("the echo", 60*time.Second, func(p ui.Probe) bool { return strings.Contains(p.Terminal, "from-the-dashboard\n") })

	if mode == "live" {
		return rep
	}
	// Reload: the containers come back.
	t1 := time.Now()
	if _, err := page.Reload(); err != nil {
		t.Fatal(err)
	}
	d.ready()
	rep.WarmReadyMs = float64(time.Since(t1).Milliseconds())
	d.until("web restored", 30*time.Second, func(p ui.Probe) bool { return slices.Contains(p.Containers, "web") })

	if mode == "service-worker" {
		// Offline: the shell, dashboard.wasm included, from Cache Storage.
		if err := ctx.SetOffline(true); err != nil {
			t.Fatal(err)
		}
		t2 := time.Now()
		if _, err := page.Reload(); err != nil {
			t.Fatalf("%s: offline reload: %v", d.who, err)
		}
		d.ready()
		rep.OfflineReadyMs = float64(time.Since(t2).Milliseconds())
		d.until("web offline", 30*time.Second, func(p ui.Probe) bool { return slices.Contains(p.Containers, "web") })
		ctx.SetOffline(false)
	}
	return rep
}

// TestDashboard drives the default page in Chromium (service worker and
// headers) and Firefox (headers: Playwright's Firefox runs no service
// workers); with E2E_URL, the deployed page in Chromium.
func TestDashboard(t *testing.T) {
	pw, err := playwright.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Stop()
	cases := []struct{ browser, mode string }{
		{"chromium", "service-worker"},
		{"chromium", "headers"},
		{"chromium", "headers-dpr2"},
		{"firefox", "headers"},
	}
	if os.Getenv("E2E_URL") != "" {
		cases = cases[:0]
		cases = append(cases, struct{ browser, mode string }{"chromium", "live"})
	}
	var reports []DashReport
	for _, c := range cases {
		if os.Getenv("E2E_BROWSERS") != "" && !strings.Contains(os.Getenv("E2E_BROWSERS"), c.browser) {
			continue
		}
		t.Run(c.browser+"/"+c.mode, func(t *testing.T) {
			r := dashboard(t, pw, c.browser, c.mode)
			t.Logf("%+v", r)
			reports = append(reports, r)
		})
	}
	if p := os.Getenv("E2E_DASH_REPORT"); p != "" {
		b, _ := json.MarshalIndent(reports, "", "  ")
		os.WriteFile(p, b, 0o644)
	}
}
