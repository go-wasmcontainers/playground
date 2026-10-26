//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"

	"github.com/go-wasmcontainers/relay/auth"
	"github.com/go-wasmcontainers/relay/driver"
	v1 "github.com/go-wasmcontainers/relay/gen/relayv1"
	"github.com/go-wasmcontainers/relay/relay"
	"github.com/go-wasmcontainers/relay/server"
)

// cors answers the browser's preflight for the relay's Connect API. The
// relay (v0.2.1) sends no CORS headers yet, which a page needs to call
// JoinService from another origin; until it does, the test adds them.
func cors(origin string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o == origin {
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "POST")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Connect-Protocol-Version, Connect-Timeout-Ms")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

// TestClass joins a relay class from the page (the dialog: relay address,
// reload, name and join code), runs a web server on the class network in
// the browser, reaches it from a native session of the same class, and
// leaves. The code is checked to be nowhere the page keeps things.
func TestClass(t *testing.T) {
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
		t.Run(c.browser+"/"+c.mode, func(t *testing.T) { classSession(t, pw, c.browser, c.mode) })
	}
}

func classSession(t *testing.T, pw *playwright.Playwright, browserName, mode string) {
	url, _ := serveCopy(t, mode == "headers")
	pageOrigin := strings.TrimSuffix(url[:strings.Index(url[len("http://"):], "/")+len("http://")], "/")

	k, _ := auth.NewKeys()
	r, err := relay.New(relay.Config{Keys: k})
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := auth.RandomToken()
	s, err := server.New(server.Config{Relay: r, AdminToken: tok, OriginPatterns: []string{strings.TrimPrefix(pageOrigin, "http://")}})
	if err != nil {
		t.Fatal(err)
	}
	pub := httptest.NewTLSServer(cors(pageOrigin, s.Public()))
	defer pub.Close()
	class, err := r.CreateClass(relay.ClassSpec{Name: "net101", Template: v1.Template_TEMPLATE_NEUTRON_DEFAULT})
	if err != nil {
		t.Fatal(err)
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
	// The relay's test certificate: the browser path needs a certificate
	// the browser trusts (see README); here the context accepts it.
	ctx, err := browser.NewContext(playwright.BrowserNewContextOptions{IgnoreHttpsErrors: playwright.Bool(true)})
	if err != nil {
		t.Fatal(err)
	}
	page, err := ctx.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	b := tab{t, page, browserName + "/" + mode + " (class)"}
	var console []string
	page.On("console", func(m playwright.ConsoleMessage) { console = append(console, m.Text()) })
	if _, err := page.Goto(url + "?ui=terminal"); err != nil {
		t.Fatal(err)
	}
	b.ready()

	// Step 1: the relay address; the page reloads with the dialog reopened.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v\nconsole: %q", b.who, err, console)
		}
	}
	must(page.Locator("#joinbtn").Click())
	must(page.Locator("#relay").Fill("http://not-https.example"))
	must(page.Locator("#joinok").Click())
	b.until("http refused", `() => document.getElementById("joinerr").textContent.includes("https only")`, 5*time.Second)
	must(page.Locator("#relay").Fill(pub.URL))
	must(page.Locator("#joinok").Click())
	b.until("the dialog after the reload", `() => document.getElementById("joindlg").open && !document.getElementById("joinstep2").hidden`, 60*time.Second)
	b.ready()
	// Step 2: name and code.
	must(page.Locator("#joinname").Fill("alice"))
	must(page.Locator("#joincode").Fill(class.JoinCode))
	t0 := time.Now()
	must(page.Locator("#joinok").Click())
	b.until("joined", `() => window.__playground.state.class.state === "joined"`, 30*time.Second)
	joinMs := time.Since(t0).Milliseconds()
	var label string
	label, _ = b.eval(`() => window.__playground.state.class.label`).(string)
	if label == "" {
		t.Fatalf("%s: no label", b.who)
	}
	// The code is nowhere the page keeps things.
	kept := b.eval(`() => JSON.stringify([localStorage, sessionStorage, document.getElementById("joincode").value, location.href, window.__playground.state])`).(string)
	if strings.Contains(kept, class.JoinCode) {
		t.Fatalf("%s: the join code is kept: %s", b.who, kept)
	}

	// Only the engine worker may reach the relay: the page's CSP still
	// says connect-src 'self' (a control: the same fetch the worker makes).
	if v := b.eval(fmt.Sprintf(`async () => { try { await fetch(%q); return "reached"; } catch (e) { return "blocked"; } }`, pub.URL)); v != "blocked" {
		t.Fatalf("%s: the page itself reached the relay: %v", b.who, v)
	}
	b.must("wctr network ls", 0, "class", "relay")
	b.must("wctr run -d --name web --network class net serve", 0)

	// A native session of the same class reaches the browser's container.
	bob := nativeSession(t, r, class.JoinCode, "bob")
	defer bob.Close()
	ep, err := bob.Attach(context.Background(), "probe")
	if err != nil {
		t.Fatal(err)
	}
	var body string
	deadline := time.Now().Add(30 * time.Second)
	for {
		body, err = getThrough(ep, "web."+label)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil || !strings.Contains(body, "hello bob from 10.") {
		t.Fatalf("%s: GET web.%s through the relay: %q %v", b.who, label, body, err)
	}
	t.Logf("%s: joined in %d ms; native GET through the relay: %q", b.who, joinMs, strings.TrimSpace(body))

	// A reload rejoins with the kept token (no code).
	if _, err := page.Reload(); err != nil {
		t.Fatal(err)
	}
	b.ready()
	b.until("rejoined", `() => window.__playground.state.class.state === "joined"`, 30*time.Second)

	// Leave: the network goes, and after a reload the relay is forgotten.
	must(page.Locator("#leavebtn").Click())
	b.until("left", `() => window.__playground.state.class.state === "left"`, 30*time.Second)
	if _, err := page.Reload(); err != nil {
		t.Fatal(err)
	}
	b.ready()
	if out := b.must("wctr network ls", 0); strings.Contains(out, "class") {
		t.Fatalf("%s: class network after leaving:\n%s", b.who, out)
	}
	if v := b.eval(`() => localStorage.getItem("wctr-relay")`); v != nil {
		t.Fatalf("%s: relay remembered after leaving: %v", b.who, v)
	}
}

func nativeSession(t *testing.T, r *relay.Relay, code, name string) *driver.Session {
	t.Helper()
	j, err := r.Join(code, name, nil)
	if err != nil {
		t.Fatal(err)
	}
	cl, err := r.Authenticate(j.Token)
	if err != nil {
		t.Fatal(err)
	}
	a, b := relay.Pipe()
	go r.Serve(context.Background(), b, cl)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := driver.Dial(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// getThrough GETs http://host/bob over the relay endpoint.
func getThrough(ep *driver.Endpoint, host string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ips, err := ep.Resolve(ctx, host)
	if err != nil {
		return "", err
	}
	c, err := ep.Dial(ctx, "tcp", netip.AddrPortFrom(ips[0], 80))
	if err != nil {
		return "", err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(c, "GET /bob HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String(), nil
}
