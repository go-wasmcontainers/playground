//go:build e2e

package e2e

import (
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
	defer pw.Stop()
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true), Timeout: playwright.Float(180000)})
	if err != nil {
		t.Fatalf("launch: %v", err)
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
	b := tab{t, page, "chromium/ports"}
	if _, err := page.Goto(play + "?ui=terminal&usercontent=" + url.QueryEscape(uc)); err != nil {
		t.Fatal(err)
	}
	b.ready()
	b.must("wctr run -d -p 8080:80 --name web net serve", 0)
	b.until("the ports bar lists 8080", `() => window.wctrPorts.state.ports.includes(8080)`, 15*time.Second)

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
