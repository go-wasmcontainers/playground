package usercontent

import (
	"os"
	"regexp"
	"testing"

	"github.com/go-wasmcontainers/playground/internal/portroute"
)

// The service worker relays the methods the engine's route accepts, no more
// and no fewer: a method one side has and the other lacks is either refused
// in the page for no reason (WebDAV's PROPFIND was, at first) or let through
// to a check that refuses it later.
func TestServiceWorkerMethodsMatchTheRoute(t *testing.T) {
	sw, err := os.ReadFile("assets/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	set := regexp.MustCompile(`(?s)const METHODS = new Set\(\[(.*?)\]\);`).FindSubmatch(sw)
	if set == nil {
		t.Fatal("sw.js has no METHODS set")
	}
	inJS := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([A-Z]+)"`).FindAllSubmatch(set[1], -1) {
		inJS[string(m[1])] = true
	}
	if len(inJS) < 7 {
		t.Fatalf("read %d methods from sw.js: the parse cannot be trusted", len(inJS))
	}
	for m := range portroute.Methods {
		if !inJS[m] {
			t.Errorf("%s: relayed by the route, refused by sw.js", m)
		}
	}
	for m := range inJS {
		if !portroute.Methods[m] {
			t.Errorf("%s: in sw.js, refused by the route", m)
		}
	}
	for _, webdav := range []string{"PROPFIND", "MKCOL", "MOVE", "COPY"} {
		if !inJS[webdav] {
			t.Errorf("%s is not relayed: a WebDAV client in the page cannot work", webdav)
		}
	}
}
