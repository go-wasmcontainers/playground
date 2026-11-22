package site

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wasmdesk/wasmbox/audioring"
)

// TestBuildServeVerify builds the site (without the pkgx pilots, which
// need the network), serves it like Pages, and checks Verify both ways.
func TestBuildServeVerify(t *testing.T) {
	out := filepath.Join(t.TempDir(), "site")
	var log bytes.Buffer
	m, err := Build(context.Background(), Options{Root: "../..", Out: out, Version: "test", Log: &log})
	if err != nil {
		t.Fatal(err, log.String())
	}
	for _, f := range []string{"index.html", "engine.wasm", "wasm_exec.js", "container-worker.js", "busy.js", "engine-worker.js", "sw.js", "app.js",
		"vendor/xterm.js", "catalog.json", "v2/library/hello/manifests/latest"} {
		if _, ok := m.Files[f]; !ok {
			t.Errorf("site lacks %s", f)
		}
	}
	page, _ := os.ReadFile(filepath.Join(out, "index.html"))
	if bytes.Contains(page, []byte("{{")) || !bytes.Contains(page, []byte("?v="+m.Build)) {
		t.Fatal("index.html not stamped with the build")
	}
	// The service worker names the build (a new build is a new worker)
	// and lists the shell: the page and its assets, not the image mirror.
	sw, _ := os.ReadFile(filepath.Join(out, "sw.js"))
	if bytes.Contains(sw, []byte("{{")) || !bytes.Contains(sw, []byte(`const BUILD = "`+m.Build+`"`)) {
		t.Fatal("sw.js not stamped with the build")
	}
	for _, want := range []string{`[""`, `"engine.wasm"`, `"app.js"`, `"vendor/xterm.js"`, `"catalog.json"`} {
		if !bytes.Contains(sw, []byte(want)) {
			t.Errorf("sw.js shell lacks %s", want)
		}
	}
	if bytes.Contains(sw, []byte(`"v2/`)) || bytes.Contains(sw, []byte(`"sw.js"`)) {
		t.Error("the shell lists the mirror or the worker itself")
	}
	srv := httptest.NewServer(Handler(out, "/playground", true))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/playground/")
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Cross-Origin-Embedder-Policy") != "require-corp" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatal(resp, err)
	}
	resp.Body.Close()
	// The view tab: not isolated (it embeds the usercontent shell), not
	// frameable; the service worker says the same, word for word.
	resp, err = http.Get(srv.URL + "/playground/view.html")
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Cross-Origin-Embedder-Policy") != "" || resp.Header.Get("Content-Security-Policy") != ViewCSP {
		t.Fatalf("view.html: %v %v", resp, err)
	}
	resp.Body.Close()
	for _, want := range []string{`"view.html"`, `"view.js"`, `"ports.js"`} {
		if !bytes.Contains(sw, []byte(want)) {
			t.Errorf("sw.js shell lacks %s", want)
		}
	}
	if !bytes.Contains(bytes.ReplaceAll(sw, []byte("\" +\n  \""), nil), []byte(ViewCSP)) {
		t.Error("sw.js VIEW_CSP differs from site.ViewCSP")
	}
	// The audio worklet is wasmbox's embedded asset byte for byte, served
	// with the isolation headers the page's other scripts get (a worklet module
	// is a COEP-checked subresource) and listed in the shell.
	resp, err = http.Get(srv.URL + "/playground/" + AudioWorkletName)
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Cross-Origin-Embedder-Policy") != "require-corp" || resp.Header.Get("Cross-Origin-Resource-Policy") == "" {
		t.Fatalf("%s: %v %v", AudioWorkletName, resp, err)
	}
	served, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(served, audioring.WorkletJS) || len(served) == 0 {
		t.Error("the served worklet differs from audioring.WorkletJS")
	}
	for _, want := range []string{`"audio.js"`, `"` + AudioWorkletName + `"`} {
		if !bytes.Contains(sw, []byte(want)) {
			t.Errorf("sw.js shell lacks %s", want)
		}
	}
	if r, _ := http.Get(srv.URL + "/elsewhere"); r.StatusCode != 404 {
		t.Fatal("served outside the prefix")
	}
	// A deadline, so that a Verify that never returns fails here instead of hanging.
	vctx, vcancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer vcancel()
	if _, err := Verify(vctx, srv.URL+"/playground/", m, time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	// A changed file is never "live".
	os.WriteFile(filepath.Join(out, "app.js"), []byte("tampered"), 0o644)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var vlog bytes.Buffer
	if _, err := Verify(ctx, srv.URL+"/playground", m, 50*time.Millisecond, &vlog); err == nil || !strings.Contains(vlog.String(), "app.js differs") {
		t.Fatal(err, vlog.String())
	}
}
