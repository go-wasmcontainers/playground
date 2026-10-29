//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/go-wasmcontainers/playground/internal/site"
)

// PersistReport is what a persistence run measured.
type PersistReport struct {
	Browser, Mode string
	// Engine ready, first visit and after a reload (ms).
	ColdReadyMs, WarmReadyMs float64
	// The first run of lab, then the first run after the reload: where
	// the compiled module came from and how long getting it took.
	ColdCompile, WarmCompile compile
	OfflineReadyMs           float64 `json:",omitempty"`
	// A CPU-bound run (lab hog) after a reload: its module compiled from
	// bytes (the default), or (?codecache=1) from Cache Storage through
	// compileStreaming; and the first visit's, for reference.
	HogColdMs, HogCacheMs, HogNoCacheMs float64
	CacheCompile                        compile
}

type compile struct {
	Source    string  `json:"source"`
	Ms        float64 `json:"ms"`
	CompileMs float64 `json:"compileMs"`
	Bytes     int     `json:"bytes"`
	Error     string  `json:"error"`
}

// serveCopy serves a private copy of the site, which the test may change.
func serveCopy(t *testing.T, isolate bool) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "site")
	if err := os.CopyFS(dir, os.DirFS(siteDir)); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: site.Handler(dir, "/playground", isolate)}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return "http://" + l.Addr().String() + "/playground/", dir
}

type tab struct {
	t    *testing.T
	page playwright.Page
	who  string
}

func (b tab) eval(js string) any {
	v, err := b.page.Evaluate(js)
	if err != nil {
		b.t.Fatalf("%s: %s: %v", b.who, js, err)
	}
	return v
}

func (b tab) screen() string { s, _ := b.eval(`() => window.__playground.text()`).(string); return s }

func num(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case float64:
		return n
	}
	return -1
}

// until polls cond (a JS expression) and fails after d.
func (b tab) until(what, cond string, d time.Duration) float64 {
	b.t.Helper()
	t0 := time.Now()
	for {
		if v, err := b.page.Evaluate(cond); err == nil && v == true {
			return float64(time.Since(t0).Microseconds()) / 1000
		}
		if time.Since(t0) > d {
			b.t.Fatalf("%s: %s after %v; screen:\n%s", b.who, what, d, b.screen())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (b tab) ready() float64 {
	return b.until("engine not ready", `() => !!(window.__playground && window.__playground.state.ready)`, 90*time.Second)
}

// run types a line and returns its exit code and what it printed.
func (b tab) run(line string) (int, string) {
	e, out := b.runEntry(line)
	return e.Code, out
}

func (b tab) runEntry(line string) (entry, string) {
	b.t.Helper()
	n := int(num(b.eval(`() => window.__playground.state.transcript.length`)))
	before := b.screen()
	if err := b.page.Keyboard().Type(line); err != nil {
		b.t.Fatal(err)
	}
	b.page.Keyboard().Press("Enter")
	b.until(line+" did not finish", fmt.Sprintf(`() => window.__playground.state.transcript.length > %d`, n), 2*time.Minute)
	var e entry
	raw, _ := json.Marshal(b.eval(fmt.Sprintf(`() => window.__playground.state.transcript[%d]`, n)))
	json.Unmarshal(raw, &e)
	return e, strings.TrimPrefix(b.screen(), before)
}

// hog times a CPU-bound guest: lab hog fills memory byte by byte until
// the 64 MiB limit, a tight loop whose speed depends on the tier of the
// compiled code.
func (b tab) hog() float64 {
	b.t.Helper()
	e, out := b.runEntry("wctr run --rm -m 64m lab hog")
	if e.Code != 2 || !strings.Contains(out, "out of memory") {
		b.t.Fatalf("%s: hog: %d\n%s", b.who, e.Code, out)
	}
	return e.Ms
}

func (b tab) must(line string, code int, want ...string) string {
	b.t.Helper()
	c, out := b.run(line)
	if c != code {
		b.t.Fatalf("%s: %q exited %d, want %d:\n%s", b.who, line, c, code, out)
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			b.t.Fatalf("%s: %q: output lacks %q:\n%s", b.who, line, w, out)
		}
	}
	return out
}

func (b tab) lastCompile() compile {
	var cs []compile
	raw, _ := json.Marshal(b.eval(`() => window.__playground.state.compiles`))
	json.Unmarshal(raw, &cs)
	if len(cs) == 0 {
		b.t.Fatalf("%s: no compile reported", b.who)
	}
	return cs[len(cs)-1]
}

func persistence(t *testing.T, pw *playwright.Playwright, browserName, mode string) PersistReport {
	sw := mode == "service-worker"
	url, dir := serveCopy(t, !sw)
	url += "?ui=terminal" // the xterm.js page; the dashboard has its own test
	bt := pw.Chromium
	if browserName == "firefox" {
		bt = pw.Firefox
	}
	browser, err := bt.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true), Timeout: playwright.Float(180000)})
	if err != nil {
		t.Fatalf("launch %s: %v", browserName, err)
	}
	defer browser.Close()
	ctx, err := browser.NewContext()
	if err != nil {
		t.Fatal(err)
	}
	page, err := ctx.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	b := tab{t, page, browserName + "/" + mode}
	rep := PersistReport{Browser: browserName, Mode: mode}

	t0 := time.Now()
	if _, err := page.Goto(url); err != nil {
		t.Fatal(err)
	}
	b.ready()
	rep.ColdReadyMs = float64(time.Since(t0).Milliseconds())

	b.must("wctr pull lab", 0, "Downloaded newer image for lab:latest")
	b.must("wctr run --name keep lab write /data.txt hello", 0)
	rep.ColdCompile = b.lastCompile()
	rep.HogColdMs = b.hog()
	b.must("wctr run -d --name bg lab count 1000 100", 0)
	b.must("wctr run -d --rm --name gone lab count 1000 100", 0)
	b.must("wctr run --name failed lab exit 3", 3)
	time.Sleep(2500 * time.Millisecond) // a running checkpoint (every 2 s) with some counted lines

	// Reload: the engine comes back with its images and containers.
	t1 := time.Now()
	if _, err := page.Reload(); err != nil {
		t.Fatal(err)
	}
	b.ready()
	rep.WarmReadyMs = float64(time.Since(t1).Milliseconds())
	restored, _ := json.Marshal(b.eval(`() => window.__playground.state.restored`))
	if string(restored) != `{"repaired":0,"restored":3,"stopped":2}` {
		t.Fatalf("%s: restored %s", b.who, restored)
	}
	b.until("no restore notice", `() => window.__playground.text().includes("Restored 3 container(s) from your last visit")`, 10*time.Second)
	ps := b.must("wctr ps -a", 0, "keep", "Exited (0)", "bg", "Exited (137)", "failed", "Exited (3)")
	if strings.Contains(ps, "gone") {
		t.Fatalf("%s: --rm container kept:\n%s", b.who, ps)
	}
	b.must("wctr diff keep", 0, "A /data.txt")
	b.must("wctr logs bg", 0, "1\n2\n3")
	b.must("wctr logs failed", 0, "bye")
	b.must("wctr images", 0, "lab")
	b.must("wctr system df", 0, "State store: IndexedDB")
	// The module again, compiled by a fresh engine.
	b.must("wctr run --rm lab echo warm", 0, "warm")
	rep.WarmCompile = b.lastCompile()
	if rep.WarmCompile.Source != "bytes" {
		t.Errorf("%s: by default the module came from %q, want bytes", b.who, rep.WarmCompile.Source)
	}
	rep.HogNoCacheMs = b.hog()
	// The same with the compiled-module cache (?codecache=1): one visit
	// stores the module, the next compiles it from Cache Storage.
	for i, want := range []string{"stored", "cache"} {
		if _, err := page.Goto(url + "&codecache=1"); err != nil {
			t.Fatal(err)
		}
		b.ready()
		b.must("wctr run --rm lab echo codecache", 0, "codecache")
		c := b.lastCompile()
		if c.Source != want {
			t.Errorf("%s: codecache visit %d: module from %q (%s), want %q", b.who, i, c.Source, c.Error, want)
		}
		if want == "cache" {
			rep.CacheCompile = c
			rep.HogCacheMs = b.hog()
		}
		time.Sleep(300 * time.Millisecond) // the background store
	}
	if _, err := page.Goto(url); err != nil {
		t.Fatal(err)
	}
	b.ready()
	// A restored container runs again.
	b.must("wctr start failed", 0)
	b.until("failed did not run again", `() => window.__playground.state.transcript.length > 0`, time.Second)

	if sw {
		// Offline: the shell comes from Cache Storage, images and
		// containers from IndexedDB.
		if err := ctx.SetOffline(true); err != nil {
			t.Fatal(err)
		}
		t2 := time.Now()
		if _, err := page.Reload(); err != nil {
			t.Fatalf("%s: offline reload: %v", b.who, err)
		}
		b.ready()
		rep.OfflineReadyMs = float64(time.Since(t2).Milliseconds())
		b.must("wctr run --rm lab echo offline", 0, "offline")
		b.must("wctr ps -a", 0, "keep")
		ctx.SetOffline(false)

		// An update: a byte-different sw.js names a new build; it installs
		// and waits until the page is asked to switch.
		old := b.eval(`() => window.__playground.state.build`).(string)
		next := "e2enext00000"
		for _, f := range []string{"sw.js", "index.html"} {
			p := filepath.Join(dir, f)
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(raw, []byte(old)) {
				t.Fatalf("%s does not name build %s", f, old)
			}
			os.WriteFile(p, bytes.ReplaceAll(raw, []byte(old), []byte(next)), 0o644)
		}
		b.eval(`() => window.__playground.checkUpdate && window.__playground.checkUpdate()`)
		b.until("no update offered", `() => window.__playground.state.update === true`, 30*time.Second)
		if got := b.eval(`() => window.__playground.state.build`); got != old {
			t.Fatalf("%s: switched to %v before being asked", b.who, got)
		}
		if err := page.Locator("#notice button").Click(); err != nil {
			t.Fatal(err)
		}
		b.until("not on the new build", fmt.Sprintf(`() => !!(window.__playground && window.__playground.state.build === %q)`, next), 30*time.Second)
		b.ready()
		b.must("wctr ps -a", 0, "keep")
	}

	// A second tab of the same browser: one engine owns the store.
	p2, err := ctx.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	b2 := tab{t, p2, b.who + " (tab 2)"}
	if _, err := p2.Goto(url); err != nil {
		t.Fatal(err)
	}
	b2.until("tab 2 did not see tab 1", `() => document.getElementById("notice").textContent.includes("already running in another tab")`, 30*time.Second)
	if b2.eval(`() => window.__playground.state.owner`) != false {
		t.Fatal("two owners")
	}
	if err := p2.Locator("#notice button").Click(); err != nil {
		t.Fatal(err)
	}
	b2.ready()
	p2.BringToFront()
	b.until("tab 1 did not stop", `() => window.__playground.state.owner === false && document.getElementById("notice").textContent.includes("Another tab took over")`, 30*time.Second)
	b2.must("wctr ps -a", 0, "keep")
	return rep
}

func TestPersistence(t *testing.T) {
	if os.Getenv("E2E_URL") != "" {
		t.Skip("E2E_URL set: TestLive only")
	}
	pw, err := playwright.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Stop()
	var reports []PersistReport
	for _, c := range []struct{ browser, mode string }{
		{"chromium", "service-worker"},
		{"chromium", "headers"},
		{"firefox", "headers"},
	} {
		if os.Getenv("E2E_BROWSERS") != "" && !strings.Contains(os.Getenv("E2E_BROWSERS"), c.browser) {
			continue
		}
		t.Run(c.browser+"/"+c.mode, func(t *testing.T) {
			r := persistence(t, pw, c.browser, c.mode)
			t.Logf("%+v", r)
			reports = append(reports, r)
		})
	}
	if p := os.Getenv("E2E_PERSIST_REPORT"); p != "" {
		b, _ := json.MarshalIndent(reports, "", "  ")
		os.WriteFile(p, b, 0o644)
	}
}
