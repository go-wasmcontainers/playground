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
	got := CSPFor(u("/playground/engine-worker.js?v=abc&relay=https://relay.example:8443"))
	if !strings.Contains(got, "connect-src 'self' https://relay.example:8443 wss://relay.example:8443;") {
		t.Errorf("engine worker: %s", got)
	}
	for _, bad := range []string{
		"http://relay.example", "https://relay.example/", "https://relay.example;script-src *",
		"https://relay.example wss://*", "https://*.example", "https://RELAY.example", "https://relay.example:99999999",
		"https://a'b", "https://relay.example\nx", "", "javascript:alert(1)",
	} {
		q := url.Values{"relay": {bad}}
		if got := CSPFor(u("/playground/engine-worker.js?" + q.Encode())); got != CSP {
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
		if got == CSP {
			return
		}
		if strings.Count(got, ";") != strings.Count(CSP, ";") || strings.ContainsAny(relay, " ;,'\"\n\r\t*") {
			t.Fatalf("%q gave %q", relay, got)
		}
	})
}
