package site

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTree writes files (path -> content) under a new directory.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, c := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func shaHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// A temp directory that cannot be made stops the build before it builds
// anything; it never falls back to building into the working directory.
func TestBuildNeedsATempDir(t *testing.T) {
	out := filepath.Join(t.TempDir(), "site")
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	_, err := Build(context.Background(), Options{Root: "../..", Out: out, Version: "test"})
	// The refusal is the temp directory's own, not a later go build's.
	if err == nil || !strings.Contains(err.Error(), "missing") || strings.HasPrefix(err.Error(), "go build") {
		t.Fatalf("Build with no temp dir: %v", err)
	}
}

func TestSmokeTools(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "spec.json")
	os.WriteFile(good, []byte(`{}`), 0o644)
	unknown := filepath.Join(dir, "unknown.json")
	os.WriteFile(unknown, []byte(`{"nope":1}`), 0o644)
	bin := t.TempDir()
	wt := filepath.Join(bin, "wasmtime")
	os.WriteFile(wt, []byte("#!/bin/sh\n"), 0o755)

	t.Setenv("PATH", bin)
	if _, got, err := smokeTools(good); err != nil || got != wt {
		t.Errorf("smokeTools = %q, %v; want %q", got, err, wt)
	}
	if _, _, err := smokeTools(filepath.Join(dir, "missing.json")); err == nil || !os.IsNotExist(unwrapPath(err)) {
		t.Errorf("missing spec: %v", err)
	}
	if _, _, err := smokeTools(unknown); err == nil || !strings.Contains(err.Error(), unknown) || !strings.Contains(err.Error(), "nope") {
		t.Errorf("unknown field: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, _, err := smokeTools(good); err == nil || !strings.Contains(err.Error(), "--smoke needs wasmtime on PATH") {
		t.Errorf("no wasmtime: %v", err)
	}
}

func unwrapPath(err error) error {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err
	}
	return err
}

func TestCopyExplorerPages(t *testing.T) {
	out := t.TempDir()
	if err := copyExplorerPages("../..", out); err != nil {
		t.Fatal(err)
	}
	for _, to := range ExplorerFiles {
		if b, err := os.ReadFile(filepath.Join(out, to)); err != nil || len(b) == 0 {
			t.Errorf("%s: %d bytes, %v", to, len(b), err)
		}
	}
	// A root that does not require the explorer module.
	if err := copyExplorerPages(t.TempDir(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "go list -m "+ExplorerModule) {
		t.Errorf("no module: %v", err)
	}
	// An output that cannot hold the files.
	blocked := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocked, nil, 0o644)
	if err := copyExplorerPages("../..", filepath.Join(blocked, "sub")); err == nil {
		t.Error("copied into a path below a file")
	}
}

func TestCopyWasmExec(t *testing.T) {
	out := t.TempDir()
	if err := copyWasmExec(out); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(out, "wasm_exec.js")); err != nil || len(b) == 0 {
		t.Fatalf("wasm_exec.js: %d bytes, %v", len(b), err)
	}
	t.Setenv("PATH", t.TempDir()) // no go command
	if err := copyWasmExec(t.TempDir()); err == nil {
		t.Error("copyWasmExec without a go command")
	}
}

func TestCopyTree(t *testing.T) {
	src := writeTree(t, map[string]string{"a.js": "A", "vendor/b.js": "B"})
	dst := t.TempDir()
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"a.js": "A", "vendor/b.js": "B"} {
		if b, err := os.ReadFile(filepath.Join(dst, p)); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v", p, b, err)
		}
	}
	if err := copyTree(filepath.Join(src, "missing"), t.TempDir()); err == nil {
		t.Error("copied a directory that is not there")
	}
}

func siteFiles(over map[string]string) map[string]string {
	f := map[string]string{
		"index.html": "<html>{{BUILD}} {{VERSION}}</html>",
		"sw.js":      "const BUILD = \"{{BUILD}}\"; const SHELL = {{SHELL}};",
		"app.js":     "app",
		"v2/m.json":  "mirror",
	}
	for k, v := range over {
		if v == "\x00" {
			delete(f, k)
		} else {
			f[k] = v
		}
	}
	return f
}

func TestStamp(t *testing.T) {
	// A build.json left by an earlier build is neither hashed nor counted in the build id.
	files := siteFiles(map[string]string{"build.json": "{\"stale\":true}"})
	out := writeTree(t, files)
	m, err := stamp(out, "v9")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Files["build.json"]; ok {
		t.Error("build.json is in its own manifest")
	}
	clean := writeTree(t, siteFiles(nil))
	m2, err := stamp(clean, "v9")
	if err != nil {
		t.Fatal(err)
	}
	if m.Build != m2.Build || len(m.Build) != 12 {
		t.Errorf("build id depends on a stale build.json: %q vs %q", m.Build, m2.Build)
	}
	page, _ := os.ReadFile(filepath.Join(out, "index.html"))
	if string(page) != "<html>"+m.Build+" v9</html>" {
		t.Errorf("index.html = %q", page)
	}
	sw, _ := os.ReadFile(filepath.Join(out, "sw.js"))
	if want := `const BUILD = "` + m.Build + `"; const SHELL = ["","app.js"];`; string(sw) != want {
		t.Errorf("sw.js = %q, want %q", sw, want)
	}
	if m.Files["sw.js"] != shaHex(string(sw)) || m.Files["app.js"] != shaHex("app") {
		t.Errorf("manifest hashes: %v", m.Files)
	}
	raw, err := os.ReadFile(filepath.Join(out, "build.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk Manifest
	if err := json.Unmarshal(raw, &onDisk); err != nil || onDisk.Build != m.Build || len(onDisk.Files) != len(m.Files) || onDisk.Files["sw.js"] != m.Files["sw.js"] {
		t.Errorf("build.json = %s (%v)", raw, err)
	}
}

func TestStampRefusals(t *testing.T) {
	cases := []struct {
		name string
		over map[string]string
		want string
	}{
		// No worker either: the refusal is the dot-file's, not a later one's.
		{"dot-file", map[string]string{".nojekyll": "", "sw.js": "\x00"}, "dot-files"},
		{"no index", map[string]string{"index.html": "\x00"}, "no such file"},
		{"no worker", map[string]string{"sw.js": "\x00"}, "no such file"},
		{"worker without BUILD", map[string]string{"sw.js": "{{SHELL}}"}, "placeholder"},
		{"worker without SHELL", map[string]string{"sw.js": "{{BUILD}}"}, "placeholder"},
		{"quote in a name", map[string]string{"it's.js": "x"}, "quote or a backslash"},
		{"backslash in a name", map[string]string{`a\b.js`: "x"}, "quote or a backslash"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := writeTree(t, siteFiles(c.over))
			if _, err := stamp(out, "v"); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("stamp: %v, want an error naming %q", err, c.want)
			}
		})
	}
	t.Run("build.json cannot be written", func(t *testing.T) {
		out := writeTree(t, siteFiles(nil))
		if err := os.Mkdir(filepath.Join(out, "build.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := stamp(out, "v"); err == nil {
			t.Error("stamp wrote build.json over a directory")
		}
	})
	t.Run("unreadable file", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads everything")
		}
		out := writeTree(t, siteFiles(nil))
		p := filepath.Join(out, "app.js")
		os.Chmod(p, 0)
		defer os.Chmod(p, 0o644)
		if _, err := stamp(out, "v"); err == nil {
			t.Error("stamp hashed a file it cannot read")
		}
	})
	t.Run("index.html unreadable after hashing", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads everything")
		}
		// hashTree reads index.html too, so this is the same refusal one step
		// earlier: the build never goes on with a page it cannot read.
		out := writeTree(t, siteFiles(nil))
		p := filepath.Join(out, "index.html")
		os.Chmod(p, 0)
		defer os.Chmod(p, 0o644)
		if _, err := stamp(out, "v"); err == nil {
			t.Error("stamp went on with an unreadable index.html")
		}
	})
}

func TestVerifyOnce(t *testing.T) {
	want := &Manifest{Build: "b1", Files: map[string]string{"a.js": shaHex("hello")}}
	mode := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "b1" {
			t.Errorf("asset asked without the build: %s", r.URL)
		}
		switch mode {
		case "500":
			w.WriteHeader(500) // the right body, the wrong status
			w.Write([]byte("hello"))
		case "cut":
			w.Header().Set("Content-Length", "1000")
			w.Write([]byte("hel"))
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		case "other":
			w.Write([]byte("HELLO"))
		default:
			w.Write([]byte("hello"))
		}
	}))
	defer srv.Close()
	base := srv.URL + "/"
	ctx := context.Background()
	if bad, err := verifyOnce(ctx, base, want); bad != "" || err != nil {
		t.Fatalf("matching site: %q, %v", bad, err)
	}
	mode = "500"
	if bad, err := verifyOnce(ctx, base, want); err != nil || !strings.HasPrefix(bad, "a.js 500") {
		t.Errorf("500 with the right body: %q, %v", bad, err)
	}
	mode = "other"
	if bad, err := verifyOnce(ctx, base, want); err != nil || bad != "a.js differs" {
		t.Errorf("other body: %q, %v", bad, err)
	}
	mode = "cut"
	if bad, err := verifyOnce(ctx, base, want); err == nil || bad != "a.js" {
		t.Errorf("body cut short: %q, %v", bad, err)
	}
	srv.Close()
	if bad, err := verifyOnce(ctx, base, want); err == nil || bad != "a.js" {
		t.Errorf("server gone: %q, %v", bad, err)
	}
}

// Build returns the refusal of its last step: a web/ without index.html is
// refused after every build step succeeded, and no manifest comes back.
func TestBuildRefusesAPageless(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the engine")
	}
	root := t.TempDir()
	real, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	top, err := os.ReadDir(real)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range top {
		if e.Name() == "web" || e.Name() == ".git" {
			continue
		}
		if err := os.Symlink(filepath.Join(real, e.Name()), filepath.Join(root, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.CopyFS(filepath.Join(root, "web"), os.DirFS(filepath.Join(real, "web"))); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "web", "index.html")); err != nil {
		t.Fatal(err)
	}
	m, err := Build(context.Background(), Options{Root: root, Out: filepath.Join(t.TempDir(), "site"), Version: "test"})
	if err == nil || m != nil || !strings.Contains(err.Error(), "index.html") {
		t.Fatalf("Build = %v, %v; want the stamp's refusal and no manifest", m, err)
	}
}

// Verify returns as soon as the site matches; a hang is a failure, not a pass.
func TestVerifyReturnsWhenLive(t *testing.T) {
	want := &Manifest{Build: "b1", Files: map[string]string{"a.js": shaHex("hello")}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) }))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	took, err := Verify(ctx, srv.URL, want, time.Millisecond, nil)
	if err != nil || took > 10*time.Second {
		t.Fatalf("Verify = %v, %v", took, err)
	}
}
