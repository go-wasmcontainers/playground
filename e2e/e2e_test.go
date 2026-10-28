//go:build e2e

// Package e2e drives the playground in real browsers (Chromium and
// Firefox, through Playwright): it types commands into the terminal and
// reads the terminal's text and the exit statuses back. Run with
//
//	go test -tags e2e ./e2e/
//
// (browsers: go run github.com/playwright-community/playwright-go/cmd/playwright install chromium firefox).
// E2E_PKGX=1 adds the pkgx pilot images (network); E2E_REPORT=<file>
// writes the measurements as JSON.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"

	"github.com/go-wasmcontainers/playground/internal/site"
)

var siteDir string

func TestMain(m *testing.M) {
	if os.Getenv("E2E_URL") != "" {
		os.Exit(m.Run()) // a deployed site: nothing to build
	}
	dir, err := os.MkdirTemp("", "e2e-site-")
	if err != nil {
		panic(err)
	}
	siteDir = filepath.Join(dir, "site")
	ctx := context.Background()
	if _, err := site.Build(ctx, site.Options{Root: "..", Out: siteDir, Pkgx: os.Getenv("E2E_PKGX") == "1", Version: "e2e", Log: os.Stderr}); err != nil {
		fmt.Fprintln(os.Stderr, "site build:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func serve(t *testing.T, isolate bool) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: site.Handler(siteDir, "/playground", isolate)}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return "http://" + l.Addr().String() + "/playground/"
}

type step struct {
	line string
	code int
	want []string
}

type entry struct {
	Line string  `json:"line"`
	Code int     `json:"code"`
	Ms   float64 `json:"ms"`
}

// Report is what a browser run measured.
type Report struct {
	Browser         string             `json:"browser"`
	Mode            string             `json:"mode"`
	EngineReadyMs   float64            `json:"engineReadyMs"`
	Runs            map[string]float64 `json:"runsMs"`
	StatRoundTripUs float64            `json:"statRoundTripMicros"`
	ModuleInIDB     string             `json:"moduleInIndexedDB"`
	// TabMs is the slowest Tab, key press to completed line on screen
	// (sampled every 5 ms).
	TabMs float64 `json:"slowestTabMs"`
	// HTTP between two containers on the bridge (net bench): sequential
	// GETs' latency, and one 8 MiB GET's throughput.
	HTTPp50Ms float64 `json:"httpP50Ms"`
	HTTPp90Ms float64 `json:"httpP90Ms"`
	HTTPMBps  float64 `json:"httpMBps"`
}

func session(t *testing.T, pw *playwright.Playwright, browserName, mode string) Report {
	isolate := mode == "headers"
	var url string
	if mode == "live" {
		url = os.Getenv("E2E_URL")
	} else {
		url = serve(t, isolate)
	}
	var bt playwright.BrowserType
	switch browserName {
	case "chromium":
		bt = pw.Chromium
	case "firefox":
		bt = pw.Firefox
	}
	browser, err := bt.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true), Timeout: playwright.Float(180000)})
	if err != nil {
		t.Fatalf("launch %s: %v", browserName, err)
	}
	defer browser.Close()
	page, err := browser.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	var console []string
	page.On("console", func(m playwright.ConsoleMessage) { console = append(console, m.Text()) })
	page.On("pageerror", func(err error) { console = append(console, "pageerror: "+err.Error()) })
	t0 := time.Now()
	if _, err := page.Goto(url + "?ui=terminal"); err != nil { // the xterm.js page
		t.Fatal(err)
	}
	ready := func() bool {
		v, err := page.Evaluate(`() => !!(window.__playground && window.__playground.state.ready)`)
		return err == nil && v == true
	}
	for !ready() {
		if time.Since(t0) > 90*time.Second {
			st, _ := page.Locator("#status").TextContent()
			t.Fatalf("%s/%s: engine not ready after 90s; status %q; console %q", browserName, mode, st, console)
		}
		time.Sleep(200 * time.Millisecond)
	}
	rep := Report{Browser: browserName, Mode: mode, EngineReadyMs: float64(time.Since(t0).Milliseconds()), Runs: map[string]float64{}}
	iso, _ := page.Evaluate(`() => crossOriginIsolated`)
	if iso != true {
		t.Fatalf("%s/%s: page not cross-origin isolated", browserName, mode)
	}

	steps := []step{
		{"help", 0, []string{"wctr pull IMAGE", "hello:latest"}},
		{"wctr pull hello", 0, []string{"Downloaded newer image for hello:latest"}},
		{"wctr run --rm hello", 0, []string{"Hello from a WebAssembly container!"}},
		{"wctr run --rm hello", 0, []string{"Hello from a WebAssembly container!"}},
		{"wctr run --rm -e NAME=browser greet", 0, []string{"Hello, browser!"}},
		{"wctr run -d --name x lab count 3 50", 0, nil},
		{"wctr run --name y lab exit 7", 7, []string{"bye", "[exit status 7]"}},
		{"wctr run --rm lab cat /etc/motd", 0, []string{"Welcome to the go-wasmcontainers playground."}},
		{"wctr run --rm -w /etc lab pwd", 0, []string{"/etc"}},
		{"wctr run --rm lab fs", 0, []string{"via link: alpha", "ok"}},
		{"wctr run --rm lab bench 1000", 0, []string{"1000 stat calls"}},
		{"wctr run --rm -m 32m lab hog", 2, []string{"out of memory"}},
		{"wctr ps -a", 0, []string{"Exited (7)", "y"}},
		{"wctr logs x", 0, []string{"1\n2\n3"}},
		{"wctr logs y", 0, []string{"bye"}},
		{"wctr images", 0, []string{"hello", "greet", "lab"}},
		{"wctr rm x y", 0, nil},
		{"wctr run --rm nosuch", 125, []string{"Unable to find image"}},
		{"docker ps", 127, []string{"wctr"}},
		{`wctr run --rm lab esc '\e]52;c;ZXZpbA==\asafe \e]8;;javascript:alert(1)\e\link\e]8;;\e\ \e[6n\e[31mred'`, 0, []string{"safe link red"}},
		// wasi-nn on the CPU backend (a host module beside
		// wasi_snapshot_preview1, through the container worker's typed
		// imports); no GPU backend in the browser yet, and it says so.
		{"wctr run --rm mnist", 0, []string{"digit 7 (p=1.000)"}},
		{"wctr run --rm mnist gpu", 1, []string{"unsupported operation"}},
	}
	// Networking: two containers by name on the default bridge, UDP, the
	// network's DNS, and a measurement (the browser's socket wait path).
	steps = append(steps,
		step{"wctr run -d --name web net serve", 0, nil},
		step{"wctr run --rm net get http://web/browser", 0, []string{"hello browser from 10.88.0."}},
		step{"wctr run -d --name echo net udp-echo", 0, nil},
		step{"wctr run --rm net udp echo:7 ping", 0, []string{"reply: ping"}},
		step{"wctr run --rm net lookup nobody", 1, []string{"no such host"}},
		step{"wctr run --rm net bench http://web 200", 0, []string{"200 requests: p50"}},
		step{"wctr rm -f web echo", 0, nil},
	)
	if os.Getenv("E2E_PKGX") == "1" {
		steps = append(steps,
			step{"wctr run --rm age --version", 0, []string{"v1.3.2"}},
			step{"wctr run --rm shfmt --version", 0, []string{"v3.14.1"}},
			// C bottles (tar+zstd); lua's error() is a wasm exception.
			step{`wctr run --rm lua -e 'print(pcall(error))'`, 0, []string{"false", "nil"}},
			step{`wctr run --rm lua -e 'print(pcall(function() error("boom") end))'`, 0, []string{"false", "(command line):1: boom"}},
			step{`wctr run --rm sqlite3 :memory: 'select 6*7;'`, 0, []string{"42"}},
		)
	}
	textBefore := ""
	for i, s := range steps {
		if err := page.Keyboard().Type(s.line); err != nil {
			t.Fatal(err)
		}
		if err := page.Keyboard().Press("Enter"); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Minute)
		var tr []entry
		for {
			v, err := page.Evaluate(`() => JSON.stringify(window.__playground.state.transcript)`)
			if err == nil {
				json.Unmarshal([]byte(v.(string)), &tr)
			}
			if len(tr) > i {
				break
			}
			if time.Now().After(deadline) {
				txt, _ := page.Evaluate(`() => window.__playground.text()`)
				t.Fatalf("%s/%s: %q did not finish\n%v\nconsole %q", browserName, mode, s.line, txt, console)
			}
			time.Sleep(50 * time.Millisecond)
		}
		e := tr[i]
		txtV, _ := page.Evaluate(`() => window.__playground.text()`)
		txt := txtV.(string)
		out := strings.TrimPrefix(txt, textBefore)
		textBefore = txt
		if e.Code != s.code {
			t.Fatalf("%s/%s: %q exited %d, want %d\n%s", browserName, mode, s.line, e.Code, s.code, out)
		}
		for _, w := range s.want {
			if !strings.Contains(out, w) {
				t.Fatalf("%s/%s: %q: output lacks %q:\n%s", browserName, mode, s.line, w, out)
			}
		}
		if strings.Contains(s.line, "lab esc") {
			raw, _ := page.Evaluate(`() => window.__playground.state.raw`)
			r := raw.(string)
			if strings.Contains(r, "\x1b]") || strings.Contains(r, "\x1b[6n") {
				t.Fatalf("a dangerous sequence reached the terminal: %q", r)
			}
			if !strings.Contains(r, "\x1b[31mred") {
				t.Fatalf("colours were filtered too: %q", r)
			}
		}
		rep.Runs[fmt.Sprintf("%02d %s", i, s.line)] = e.Ms
		if strings.Contains(s.line, "lab bench") {
			m := regexp.MustCompile(`([0-9.]+) µs each`).FindStringSubmatch(out)
			if m != nil {
				rep.StatRoundTripUs, _ = strconv.ParseFloat(m[1], 64)
			}
		}
		if strings.Contains(s.line, "net bench") {
			m := regexp.MustCompile(`p50 ([0-9.]+) ms, p90 ([0-9.]+) ms; .*: ([0-9.]+) MB/s`).FindStringSubmatch(out)
			if m == nil {
				t.Fatalf("%s/%s: no measurement in %q", browserName, mode, out)
			}
			rep.HTTPp50Ms, _ = strconv.ParseFloat(m[1], 64)
			rep.HTTPp90Ms, _ = strconv.ParseFloat(m[2], 64)
			rep.HTTPMBps, _ = strconv.ParseFloat(m[3], 64)
		}
	}
	rep.TabMs = completion(t, page, browserName+"/"+mode, len(steps))
	// Can a compiled module be stored in IndexedDB (structured clone)?
	probe, err := page.Evaluate(`async () => {
	  const mod = await WebAssembly.compile(new Uint8Array([0,97,115,109,1,0,0,0]));
	  return await new Promise((resolve) => {
	    const r = indexedDB.open("e2e-probe", 1);
	    r.onupgradeneeded = () => r.result.createObjectStore("m");
	    r.onsuccess = () => {
	      try {
	        const tx = r.result.transaction("m", "readwrite");
	        tx.objectStore("m").put(mod, "k");
	        tx.oncomplete = () => resolve("stored");
	        tx.onerror = () => resolve("error: " + tx.error);
	      } catch (e) { resolve(e.name + ": " + e.message); }
	    };
	    r.onerror = () => resolve("open failed");
	  });
	}`)
	if err == nil {
		rep.ModuleInIDB = fmt.Sprint(probe)
	}
	// A reload keeps the pulled images (IndexedDB).
	if _, err := page.Reload(); err != nil {
		t.Fatal(err)
	}
	t1 := time.Now()
	for !ready() {
		if time.Since(t1) > 60*time.Second {
			t.Fatal("not ready after reload")
		}
		time.Sleep(200 * time.Millisecond)
	}
	page.Keyboard().Type("wctr images")
	page.Keyboard().Press("Enter")
	for {
		v, _ := page.Evaluate(`() => window.__playground.state.transcript.length`)
		if n, ok := v.(int); ok && n > 0 {
			break
		}
		if f, ok := v.(float64); ok && f > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	txt, _ := page.Evaluate(`() => window.__playground.text()`)
	if !strings.Contains(txt.(string), "hello") {
		t.Fatalf("%s: images lost on reload:\n%s", browserName, txt)
	}
	return rep
}

// completion types keys as a user would and reads the terminal back: Tab,
// Tab Tab, Ctrl-U, Ctrl-R and Up. It returns the slowest Tab, from the key
// press to the completed line on screen. done is the transcript length so
// far.
func completion(t *testing.T, page playwright.Page, who string, done int) float64 {
	t.Helper()
	screen := func() string {
		v, _ := page.Evaluate(`() => window.__playground.text()`)
		s, _ := v.(string)
		return s
	}
	last := func() string {
		s := screen()
		return s[strings.LastIndexByte(s, '\n')+1:]
	}
	// until waits for the screen to satisfy ok, and says how long it took.
	until := func(what string, ok func() bool) float64 {
		t0 := time.Now()
		for !ok() {
			if time.Since(t0) > 10*time.Second {
				t.Fatalf("%s: %s; screen:\n%s", who, what, screen())
			}
			time.Sleep(5 * time.Millisecond)
		}
		return float64(time.Since(t0).Microseconds()) / 1000
	}
	press := func(keys ...string) {
		for _, k := range keys {
			if err := page.Keyboard().Press(k); err != nil {
				t.Fatal(err)
			}
		}
	}
	typ := func(s string) {
		if err := page.Keyboard().Type(s); err != nil {
			t.Fatal(err)
		}
	}
	finished := func(n int) {
		until(fmt.Sprintf("line %d did not finish", n), func() bool {
			v, _ := page.Evaluate(`() => window.__playground.state.transcript.length`)
			f, _ := v.(float64)
			i, _ := v.(int)
			return int(f) >= n || i >= n
		})
	}
	var slowest float64
	tab := func(want string) {
		press("Tab")
		if ms := until(fmt.Sprintf("Tab: line is %q, want %q", last(), want), func() bool { return last() == want }); ms > slowest {
			slowest = ms
		}
	}
	// A unique match completes with a space; flags, images and containers
	// come from the cobra tree and the engine.
	typ("wctr ru")
	tab("$ wctr run")
	typ("--rm hel")
	tab("$ wctr run --rm hello")
	press("Enter")
	finished(done + 1)
	if !strings.Contains(screen(), "Hello from a WebAssembly container!") {
		t.Fatalf("%s: completed line did not run:\n%s", who, screen())
	}
	// Tab Tab lists the candidates with their descriptions, then the line
	// comes back unchanged.
	typ("wctr run --rm lab e")
	press("Tab", "Tab")
	until("Tab Tab did not list lab's commands", func() bool {
		s := screen()
		return strings.Contains(s, "echo") && strings.Contains(s, "print the arguments") && strings.Contains(s, "exit")
	})
	until("the line did not come back after the list", func() bool { return last() == "$ wctr run --rm lab e" })
	press("Control+u")
	until("Ctrl-U did not clear the line", func() bool { return last() == "$" })
	typ("wctr logs ")
	tab("$ wctr logs") // no container left: nothing to insert, the line stays
	press("Control+u")
	// Ctrl-R finds a line typed earlier; Enter runs it.
	press("Control+r")
	typ("NAME=b")
	until("Ctrl-R did not find the greet line", func() bool {
		return strings.Contains(last(), "wctr run --rm -e NAME=browser greet")
	})
	press("Enter")
	finished(done + 2)
	// Up recalls it again.
	press("ArrowUp")
	until("Up did not recall the last line", func() bool { return last() == "$ wctr run --rm -e NAME=browser greet" })
	press("Control+u")
	until("Ctrl-U did not clear the line", func() bool { return last() == "$" })
	return slowest
}

// TestLive runs the same session against a deployed site (E2E_URL, e.g.
// https://go-wasmcontainers.github.io/playground/) in Chromium, which runs
// the isolating service worker as a visitor's browser does.
func TestLive(t *testing.T) {
	if os.Getenv("E2E_URL") == "" {
		t.Skip("set E2E_URL to test a deployed site")
	}
	pw, err := playwright.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Stop()
	r := session(t, pw, "chromium", "live")
	t.Logf("%+v", r)
}

func TestBrowsers(t *testing.T) {
	if os.Getenv("E2E_URL") != "" {
		t.Skip("E2E_URL set: TestLive only")
	}
	pw, err := playwright.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Stop()
	var reports []Report
	for _, c := range []struct{ browser, mode string }{
		{"chromium", "service-worker"}, // what GitHub Pages gets
		{"chromium", "headers"},
		{"firefox", "headers"}, // Playwright's Firefox runs no service workers
	} {
		if os.Getenv("E2E_BROWSERS") != "" && !strings.Contains(os.Getenv("E2E_BROWSERS"), c.browser) {
			continue
		}
		t.Run(c.browser+"/"+c.mode, func(t *testing.T) {
			r := session(t, pw, c.browser, c.mode)
			t.Logf("%+v", r)
			reports = append(reports, r)
		})
	}
	if p := os.Getenv("E2E_REPORT"); p != "" {
		b, _ := json.MarshalIndent(reports, "", "  ")
		os.WriteFile(p, b, 0o644)
	}
}
