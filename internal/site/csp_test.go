package site

import (
	"net/url"
	"strings"
	"testing"
)

func TestCSPFor(t *testing.T) {
	u := func(s string) *url.URL {
		p, err := url.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if got := CSPFor(u("/playground/index.html?relay=https://relay.example")); got != CSP {
		t.Errorf("the page got a relay: %s", got)
	}
	// The explorer: framed by the playground only; nothing else changes.
	if got := CSPFor(u("/playground/explorer.html?relay=https://relay.example")); got != strings.Replace(CSP, "frame-ancestors 'none'", "frame-ancestors 'self'", 1) || got == CSP {
		t.Errorf("explorer.html: %s", got)
	}
	for _, p := range []string{"/playground/explorer.html.js", "/playground/explorer.wasm", "/playground/view.html"} {
		if got := CSPFor(u(p)); got != CSP {
			t.Errorf("%s may be framed: %s", p, got)
		}
	}
	got := CSPFor(u("/playground/engine-worker.js?v=abc&relay=https://relay.example:8443"))
	if !strings.Contains(got, "connect-src 'self' https://relay.example:8443 wss://relay.example:8443 https://huggingface.co https://us.aws.cdn.hf.co;") {
		t.Errorf("engine worker: %s", got)
	}
	// The model origins: exactly these two, for the engine worker only.
	want := strings.Replace(CSP, "connect-src 'self'", "connect-src 'self' https://huggingface.co https://us.aws.cdn.hf.co", 1)
	if got := CSPFor(u("/playground/engine-worker.js?v=abc")); got != want {
		t.Errorf("engine worker without a relay: %s", got)
	}
	if strings.Contains(CSP, "huggingface") || strings.Contains(CSP, "hf.co") {
		t.Errorf("the page's CSP names the model origins: %s", CSP)
	}
	for _, p := range []string{"/playground/index.html", "/playground/app.js", "/playground/sw.js", "/playground/engine-worker.js.map", "/playground/explorer.html"} {
		if got := CSPFor(u(p)); strings.Contains(got, "huggingface") || strings.Contains(got, "hf.co") {
			t.Errorf("%s got the model origins: %s", p, got)
		}
	}
	if strings.Count(want, "https://") != 2 || strings.Contains(want, "*") {
		t.Errorf("broader than two origins: %s", want)
	}
	for _, bad := range []string{
		"http://relay.example", "https://relay.example/", "https://relay.example;script-src *",
		"https://relay.example wss://*", "https://*.example", "https://RELAY.example", "https://relay.example:99999999",
		"https://a'b", "https://relay.example\nx", "", "javascript:alert(1)",
	} {
		q := url.Values{"relay": {bad}}
		if got := CSPFor(u("/playground/engine-worker.js?" + q.Encode())); got != want {
			t.Errorf("%q extended the CSP: %s", bad, got)
		}
	}
}

// FuzzCSPFor: whatever the query, the CSP changes only by one https/wss
// pair of a plain host, and keeps every other directive.
func FuzzCSPFor(f *testing.F) {
	f.Add("https://relay.example:8443")
	f.Add("https://[::1]:8443")
	f.Add("https://x;script-src *")
	f.Fuzz(func(t *testing.T, relay string) {
		got := CSPFor(&url.URL{Path: "/p/engine-worker.js", RawQuery: url.Values{"relay": {relay}}.Encode()})
		// Always the model origins, then at most one relay pair.
		base := strings.Replace(CSP, "connect-src 'self'", "connect-src 'self' "+ModelOrigins, 1)
		if got == base {
			return
		}
		if strings.Count(got, ";") != strings.Count(CSP, ";") || strings.ContainsAny(relay, " ;,'\"\n\r\t*") ||
			!strings.Contains(got, " "+ModelOrigins+";") || strings.Count(got, "https://") != 3 {
			t.Fatalf("%q gave %q", relay, got)
		}
	})
}
