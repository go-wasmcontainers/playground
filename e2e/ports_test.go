//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/go-wasmcontainers/playground/internal/usercontent"
)

// TestPublishedPort is `-p` in the browser, end to end, with two local
// origins: the playground (http://127.0.0.1:A) and the usercontent site
// (http://localhost:B), served as GitHub Pages would (no headers, 404 on
// /s/). A container publishes 8080; the ports bar opens the view tab; the
// view embeds the shell, whose service worker relays the container's page
// through the view and the playground tab to the engine, and back.
func TestPublishedPort(t *testing.T) {
	b, view, content := portView(t, "wctr run -d -p 8080:80 --name web net serve", playwright.BrowserNewContextOptions{})
	deadline := time.Now().Add(30 * time.Second)
	var text string
	for time.Now().Before(deadline) {
		if f := content(); f != nil {
			text, _ = f.TextContent("body")
			if strings.Contains(text, "hello  from ") {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(text, "hello  from ") {
		st, _ := view.Evaluate(`() => window.__wctrView`)
		t.Fatalf("the container's page never showed: %q; view %v", text, st)
	}
	publishedPortRest(t, b, view, content)
}

// TestFileshare is the go-fileshare server in the browser: the fileshare image
// (go-fileshare/fileshare built for wasip1, WebDAV only) publishes 8080, the
// view opens it, and the browser is the client. Without credentials the server
// challenges; with alice's, the share lists, a file reads, a PUT lands and
// reads back -- all through the usercontent route, nothing over a network.
func TestFileshare(t *testing.T) {
	b, view, content := portView(t, "wctr run -d -p 8080:80 --name files fileshare", playwright.BrowserNewContextOptions{})
	_ = b
	var f playwright.Frame
	deadline := time.Now().Add(60 * time.Second)
	for f == nil && time.Now().Before(deadline) {
		f = content()
		time.Sleep(100 * time.Millisecond)
	}
	if f == nil {
		st, _ := view.Evaluate(`() => window.__wctrView`)
		t.Fatalf("the container's page never opened; view %v", st)
	}
	// The port is published as soon as the container starts, but the server
	// listens only once its module (26 MB) is compiled and running: until
	// then the route answers 502. Wait for anything else.
	listening := false
	for deadline := time.Now().Add(90 * time.Second); !listening && time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		st, err := f.Evaluate(`async () => (await fetch("files/README.txt")).status`)
		listening = err == nil && num(st) != 502
	}
	if !listening {
		t.Fatal("the fileshare server never listened: the route still answers 502")
	}
	got, err := f.Evaluate(`async () => {
		const auth = {Authorization: "Basic " + btoa("alice:playground")};
		const anon = await fetch("files/README.txt");
		const wrong = await fetch("files/README.txt", {headers: {Authorization: "Basic " + btoa("alice:nope")}});
		const list = await fetch("files/", {method: "PROPFIND", headers: {...auth, Depth: "1"}});
		const listing = await list.text();
		const readme = await fetch("files/README.txt", {headers: auth});
		const put = await fetch("files/from-the-browser.txt", {method: "PUT", headers: auth, body: "written by the page"});
		const back = await fetch("files/from-the-browser.txt", {headers: auth});
		return {anon: anon.status, challenge: anon.headers.get("WWW-Authenticate") || "", wrong: wrong.status,
			list: list.status, listed: listing.includes("README.txt"),
			readme: readme.status, readmeText: (await readme.text()).slice(0, 40),
			put: put.status, back: back.status, backText: await back.text()};
	}`)
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	t.Logf("fileshare in the browser: %v", m)
	switch {
	case num(m["anon"]) != 401 || !strings.HasPrefix(fmt.Sprint(m["challenge"]), "Basic"):
		t.Fatalf("no challenge without credentials: %v", m)
	case num(m["wrong"]) != 401:
		t.Fatalf("a wrong password was accepted: %v", m)
	case num(m["list"]) != 207 || m["listed"] != true:
		t.Fatalf("PROPFIND did not list the share: %v", m)
	case num(m["readme"]) != 200 || !strings.HasPrefix(fmt.Sprint(m["readmeText"]), "This is a go-fileshare share"):
		t.Fatalf("README.txt: %v", m)
	case num(m["put"]) != 201 || num(m["back"]) != 200 || m["backText"] != "written by the page":
		t.Fatalf("a PUT did not land: %v", m)
	}
}

// TestFileshareExplorer is the go-fileshare explorer as the browser client of
// the fileshare image: the ports bar's "explore :8080" asks for a user and a
// password, and the explorer (explorer.html, in a frame of the playground)
// lists the share through its bridge, which talks to the engine directly --
// no view tab, no usercontent origin. The explorer draws on a canvas; what it
// shows comes back through the bridge's state(json).
func TestFileshareExplorer(t *testing.T) {
	if os.Getenv("E2E_URL") != "" {
		t.Skip("runs the fileshare image: a local build")
	}
	play := serve(t, true)
	pw, err := playwright.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pw.Stop() })
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true), Timeout: playwright.Float(180000)})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	t.Cleanup(func() { browser.Close() })
	page, err := browser.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	b := tab{t, page, "chromium/explorer"}
	var console []string
	page.OnConsole(func(m playwright.ConsoleMessage) { console = append(console, m.Type()+": "+m.Text()) })
	t.Cleanup(func() {
		if t.Failed() {
			st, _ := page.Evaluate(`() => JSON.stringify(window.__wctrExplore)`)
			t.Logf("explorer state: %v\nconsole:\n%s", st, strings.Join(console, "\n"))
		}
	})
	if _, err := page.Goto(play + "?ui=terminal"); err != nil {
		t.Fatal(err)
	}
	b.ready()
	b.must("wctr run -d -p 8080:80 --name files fileshare", 0)
	b.until("the ports bar lists 8080", `() => window.wctrPorts.state.ports.includes(8080)`, 30*time.Second)

	explore := func(user, pass string) {
		t.Helper()
		if err := page.Locator("#portbar button", playwright.PageLocatorOptions{HasText: "explore :8080"}).Click(); err != nil {
			t.Fatal(err)
		}
		for sel, v := range map[string]string{"#exploreuser": user, "#explorepass": pass} {
			if err := page.Locator(sel).Fill(v); err != nil {
				t.Fatal(err)
			}
		}
		if err := page.Locator("#exploreform button[type=submit]").Click(); err != nil {
			t.Fatal(err)
		}
	}

	// A wrong password: the server refuses, and the explorer says so.
	explore("alice", "nope")
	b.until("the server refused a wrong password", `() => (window.__wctrExplore.calls || []).some((c) => c.status === 401)`, 90*time.Second)
	if listed, _ := page.Evaluate(`() => ((window.__wctrExplore.explorer || {}).entries || []).length`); num(listed) != 0 {
		t.Fatal("the explorer listed the server with a wrong password")
	}
	if pw, _ := page.Evaluate(`() => document.getElementById("explorepass").value`); pw != "" {
		t.Fatal("the password stayed in the form")
	}
	if err := page.Locator("#explorerclose").Click(); err != nil {
		t.Fatal(err)
	}

	// alice's: the server lists its share.
	explore("alice", "playground")
	b.until("the explorer lists the share", `() => JSON.stringify(((window.__wctrExplore.explorer || {}).entries) || []).includes("files")`, 90*time.Second)

	// The bridge both ways: a PUT the explorer would send, read back.
	got, err := page.Evaluate(`async () => {
		const br = window.fileshareExplorerBridge;
		const put = await br.request("PUT", "/files/from-the-explorer.txt", [["Content-Type", "text/plain"]], new TextEncoder().encode("written through the bridge"));
		const back = await br.request("GET", "/files/from-the-explorer.txt", [], null);
		const evil = await br.request("GET", "/files/README.txt", [["Authorization", "Basic " + btoa("mallory:x")]], null);
		return {put: put.status, back: back.status, text: new TextDecoder().decode(back.body), evil: evil.status};
	}`)
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	t.Logf("through the bridge: %v; calls %v", m, must(page.Evaluate(`() => window.__wctrExplore.calls.slice(-6)`)))
	switch {
	case num(m["put"]) != 201 || num(m["back"]) != 200 || m["text"] != "written through the bridge":
		t.Fatalf("a PUT through the bridge did not land: %v", m)
	case num(m["evil"]) != 200:
		// The session's credentials replace whatever the frame sends.
		t.Fatalf("the frame's own Authorization was not replaced: %v", m)
	}

	// Closing forgets the session: the bridge refuses after.
	if err := page.Locator("#explorerclose").Click(); err != nil {
		t.Fatal(err)
	}
	closed, _ := page.Evaluate(`async () => { try { await window.fileshareExplorerBridge.request("GET", "/files/", [], null); return "answered"; } catch (e) { return String(e); } }`)
	if !strings.Contains(fmt.Sprint(closed), "closed") {
		t.Fatalf("the bridge answered after close: %v", closed)
	}
}

func must(v any, _ error) any { return v }

// portTab is the playground tab of a port test, and the usercontent origin.
type portTab struct {
	tab
	uc string
}

// portView serves the playground and a usercontent site on two local
// origins, runs line (which publishes 8080) in the playground's terminal,
// opens the port from the ports bar, and returns the playground tab, the view
// tab once its shell is ready, and a finder for the container's page frame.
func portView(t *testing.T, line string, ctxOpts playwright.BrowserNewContextOptions) (portTab, playwright.Page, func() playwright.Frame) {
	t.Helper()
	if os.Getenv("E2E_URL") != "" {
		t.Skip("a deployed site: its usercontent origin is the public one")
	}
	play := serve(t, true)
	pu, _ := url.Parse(play)
	playOrigin := pu.Scheme + "://" + pu.Host

	ucDir := t.TempDir()
	if err := usercontent.Render(playOrigin, ucDir, true); err != nil {
		t.Fatal(err)
	}
	files := http.FileServer(http.Dir(ucDir))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/s/") {
			http.NotFound(w, r) // only the service worker serves these, as on Pages
			return
		}
		files.ServeHTTP(w, r)
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	uc := "http://localhost:" + strings.TrimPrefix(l.Addr().String(), "127.0.0.1:")

	pw, err := playwright.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pw.Stop() })
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true), Timeout: playwright.Float(180000)})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	t.Cleanup(func() { browser.Close() })
	ctx, err := browser.NewContext(ctxOpts)
	if err != nil {
		t.Fatal(err)
	}
	page, err := ctx.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	b := tab{t, page, "chromium/ports"}
	if _, err := page.Goto(play + "?ui=terminal&usercontent=" + url.QueryEscape(uc)); err != nil {
		t.Fatal(err)
	}
	b.ready()
	b.must(line, 0)
	b.until("the ports bar lists 8080", `() => window.wctrPorts.state.ports.includes(8080)`, 30*time.Second)

	view, err := ctx.ExpectPage(func() error { return page.Locator("#portbar button").First().Click() })
	if err != nil {
		t.Fatal(err)
	}
	v := tab{t, view, "chromium/view"}
	v.until("the shell is ready", `() => !!(window.__wctrView && window.__wctrView.ready)`, 30*time.Second)
	if u := view.URL(); strings.Contains(u, "#") {
		t.Fatalf("the nonce stayed in the view's URL: %s", u)
	}
	if iso, _ := view.Evaluate(`() => crossOriginIsolated`); iso != false {
		t.Fatal("the view tab is cross-origin isolated: it could not embed the shell")
	}

	// The container's page, two frames down: the shell, then /s/<sid>/8080/.
	content := func() playwright.Frame {
		for _, f := range view.Frames() {
			if strings.HasPrefix(f.URL(), uc+"/s/") && strings.Contains(f.URL(), "/8080/") {
				return f
			}
		}
		return nil
	}
	return portTab{b, uc}, view, content
}

// publishedPortRest is the rest of TestPublishedPort, once the page showed.
func publishedPortRest(t *testing.T, b portTab, view playwright.Page, content func() playwright.Frame) {
	// A body in many chunks, and how fast the route is: 8 MiB from the
	// container, through every hop, measured in the content frame.
	f := content()
	got, err := f.Evaluate(`async () => {
		const t0 = performance.now();
		const r = await fetch("bulk?bytes=8388608");
		const n = (await r.arrayBuffer()).byteLength;
		return {status: r.status, bytes: n, ms: performance.now() - t0, cookie: document.cookie};
	}`)
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if num(m["status"]) != 200 || num(m["bytes"]) != 8388608 {
		t.Fatalf("8 MiB through the route: %v", m)
	}
	ms := num(m["ms"])
	t.Logf("8 MiB through the route in %.0f ms: %.1f MB/s", ms, 8.388608/(ms/1000))

	// Closing the view ends the session in the engine.
	n := num(b.eval(`() => window.wctrPorts.state.views.length`))
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%v views opened, want 1", n)
	}
}
