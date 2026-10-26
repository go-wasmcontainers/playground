package site

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	for _, f := range []string{"index.html", "engine.wasm", "wasm_exec.js", "container-worker.js", "engine-worker.js", "sw.js", "app.js",
		"vendor/xterm.js", "catalog.json", "v2/library/hello/manifests/latest"} {
		if _, ok := m.Files[f]; !ok {
			t.Errorf("site lacks %s", f)
		}
	}
	page, _ := os.ReadFile(filepath.Join(out, "index.html"))
	if bytes.Contains(page, []byte("{{")) || !bytes.Contains(page, []byte("?v="+m.Build)) {
		t.Fatal("index.html not stamped with the build")
	}
	srv := httptest.NewServer(Handler(out, "/playground", true))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/playground/")
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Cross-Origin-Embedder-Policy") != "require-corp" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatal(resp, err)
	}
	resp.Body.Close()
	if r, _ := http.Get(srv.URL + "/elsewhere"); r.StatusCode != 404 {
		t.Fatal("served outside the prefix")
	}
	if _, err := Verify(context.Background(), srv.URL+"/playground/", m, time.Millisecond, nil); err != nil {
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
