// Package site assembles the playground's static site, serves it for
// tests, and checks a deployed copy against a build.
package site

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/go-wasmcontainers/engine/runtime/browser"

	"github.com/go-wasmcontainers/playground/internal/mirror"
)

// Options of a build.
type Options struct {
	// Root is the playground module's root (where images/, web/ live).
	Root string
	// Out is the output directory (replaced).
	Out string
	// Pkgx adds the pkgx wasip1 pilots, fetched from ghcr.io.
	Pkgx bool
	// Version is the engine version string compiled in.
	Version string
	// Log receives progress lines.
	Log io.Writer
}

// Manifest is build.json: every served file and its sha256, so a deployed
// copy can be checked byte for byte.
type Manifest struct {
	Build string            `json:"build"`
	Files map[string]string `json:"files"`
}

func goBuild(root, pkg, out string, env []string, ldflags string) error {
	args := []string{"build", "-trimpath", "-o", out}
	if ldflags != "" {
		args = append(args, "-ldflags="+ldflags)
	}
	cmd := exec.Command("go", append(args, pkg)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = nil
	if msg, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build %s: %v\n%s", pkg, err, msg)
	}
	return nil
}

func goRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Images are the playground's own images, built from images/<name>.
var Images = []struct {
	Name, Description string
	Artifact, Index   bool
	Files             map[string]string
}{
	{Name: "hello", Description: "prints a greeting and exits"},
	{Name: "greet", Description: "greets $NAME (a CNCF wasm OCI artifact)", Artifact: true},
	{Name: "lab", Description: "a toolbox: echo, env, pwd, ls, cat, write, count, sleep, spin, hog, bench, fs, exit", Index: true,
		Files: map[string]string{"etc/motd": "Welcome to the go-wasmcontainers playground.\n"}},
}

// Build assembles the site into o.Out.
func Build(ctx context.Context, o Options) (*Manifest, error) {
	logf := func(format string, a ...any) {
		if o.Log != nil {
			fmt.Fprintf(o.Log, format+"\n", a...)
		}
	}
	if err := os.RemoveAll(o.Out); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(o.Out, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "site-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	// Images.
	var imgs []mirror.Image
	wasip1 := []string{"GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0"}
	for _, im := range Images {
		bin := filepath.Join(tmp, im.Name+".wasm")
		if err := goBuild(o.Root, "./images/"+im.Name, bin, wasip1, "-s -w"); err != nil {
			return nil, err
		}
		mod, err := os.ReadFile(bin)
		if err != nil {
			return nil, err
		}
		mi := mirror.Image{Name: im.Name, Tags: []string{"latest"}, Description: im.Description, Index: im.Index,
			Created: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)}
		if im.Artifact {
			mi.Module = mod
		} else {
			mi.Files = map[string][]byte{"bin/" + im.Name + ".wasm": mod}
			for p, c := range im.Files {
				mi.Files[p] = []byte(c)
			}
			mi.Entrypoint = []string{"/bin/" + im.Name + ".wasm"}
		}
		imgs = append(imgs, mi)
		logf("image %s: %d bytes of wasm", im.Name, len(mod))
	}
	if o.Pkgx {
		for _, b := range mirror.Pilots {
			bl, err := mirror.FetchBottle(ctx, b, 100<<20)
			if err != nil {
				return nil, err
			}
			bin := filepath.Dir(bl.WasmPath)
			imgs = append(imgs, mirror.Image{
				Name: b.Name, Tags: []string{bl.Ref.Tag, "latest"},
				Description: "pkgx bottle " + bl.Ref.Path + ":" + bl.Ref.Tag + " (" + strings.TrimPrefix(bl.WasmPath, "/") + ")",
				Layer:       bl.Layer,
				Entrypoint:  []string{bl.WasmPath},
				Env:         []string{"PATH=" + bin + ":/usr/local/bin:/usr/bin:/bin"},
				Created:     time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
			})
			logf("image %s: pkgx bottle %s, %d bytes", b.Name, b.Ref, len(bl.Layer))
		}
	}
	if err := mirror.Write(o.Out, imgs); err != nil {
		return nil, err
	}

	// The engine, the Go runtime glue, the container worker.
	ld := "-s -w -X main.version=" + o.Version
	if err := goBuild(o.Root, "./cmd/engine", filepath.Join(o.Out, "engine.wasm"), []string{"GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0"}, ld); err != nil {
		return nil, err
	}
	gr, err := goRoot()
	if err != nil {
		return nil, err
	}
	if err := copyFile(filepath.Join(gr, "lib", "wasm", "wasm_exec.js"), filepath.Join(o.Out, "wasm_exec.js")); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(o.Out, "container-worker.js"), browser.WorkerJS, 0o644); err != nil {
		return nil, err
	}
	// Static files.
	web := filepath.Join(o.Root, "web")
	err = filepath.WalkDir(web, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(web, p)
		return copyFile(p, filepath.Join(o.Out, rel))
	})
	if err != nil {
		return nil, err
	}
	// No .nojekyll: a site deployed by an Actions workflow is not built by
	// Jekyll, and Pages does not serve dot-files (a .nojekyll here 404s).
	// The build id is the hash of everything but index.html (sw.js counts
	// as its template), which then names it on every asset URL, so a cached
	// page asks for matching files.
	m, err := hashTree(o.Out)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(m.Files))
	for k := range m.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		if k != "index.html" {
			fmt.Fprintf(h, "%s %s\n", k, m.Files[k])
		}
	}
	build := hex.EncodeToString(h.Sum(nil))[:12]
	idx := filepath.Join(o.Out, "index.html")
	page, err := os.ReadFile(idx)
	if err != nil {
		return nil, err
	}
	page = bytes.ReplaceAll(page, []byte("{{BUILD}}"), []byte(build))
	page = bytes.ReplaceAll(page, []byte("{{VERSION}}"), []byte(o.Version))
	if err := os.WriteFile(idx, page, 0o644); err != nil {
		return nil, err
	}
	// The service worker keeps this build's app shell offline: it names
	// the build (so a new build is a byte-different worker, which is how a
	// browser notices it) and lists the files ("" is the page). The image
	// mirror is not part of the shell: pulled images live in IndexedDB.
	shell := []string{""}
	for _, k := range keys {
		if k != "index.html" && k != "sw.js" && !strings.HasPrefix(k, "v2/") && !strings.HasSuffix(k, ".LICENSE") {
			if strings.ContainsAny(k, `'\`) { // the list sits in a JS '…' literal
				return nil, fmt.Errorf("%s: a shell file name may not contain a quote or a backslash", k)
			}
			shell = append(shell, k)
		}
	}
	sl, _ := json.Marshal(shell)
	swp := filepath.Join(o.Out, "sw.js")
	sw, err := os.ReadFile(swp)
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(sw, []byte("{{BUILD}}")) || !bytes.Contains(sw, []byte("{{SHELL}}")) {
		return nil, fmt.Errorf("sw.js lacks its {{BUILD}} or {{SHELL}} placeholder")
	}
	sw = bytes.ReplaceAll(sw, []byte("{{BUILD}}"), []byte(build))
	sw = bytes.ReplaceAll(sw, []byte("{{SHELL}}"), sl) // JSON of plain paths: no quote to escape
	if err := os.WriteFile(swp, sw, 0o644); err != nil {
		return nil, err
	}
	m, err = hashTree(o.Out)
	if err != nil {
		return nil, err
	}
	m.Build = build
	b, _ := json.MarshalIndent(m, "", " ")
	if err := os.WriteFile(filepath.Join(o.Out, "build.json"), b, 0o644); err != nil {
		return nil, err
	}
	logf("site %s: build %s, %d files (%s)", o.Out, build, len(m.Files), runtime.Version())
	return m, nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func hashTree(dir string) (*Manifest, error) {
	m := &Manifest{Files: map[string]string{}}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if rel == "build.json" {
			return nil
		}
		for _, part := range strings.Split(rel, "/") {
			if strings.HasPrefix(part, ".") {
				return fmt.Errorf("%s: Pages does not serve dot-files", rel)
			}
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := sha256.Sum256(b)
		m.Files[rel] = hex.EncodeToString(s[:])
		return nil
	})
	return m, err
}

// CSP is the page's Content-Security-Policy: same-origin everything,
// WebAssembly compilation allowed, no eval, no inline scripts. Inline
// styles are allowed because xterm.js's DOM renderer writes its own
// <style>; no script can be injected through it.
const CSP = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; " +
	"connect-src 'self'; worker-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Handler serves dir under prefix like GitHub Pages; with isolate it also
// sends the COOP/COEP/CORP and CSP headers the service worker otherwise
// adds (Firefox under automation does not run service workers).
func Handler(dir, prefix string, isolate bool) http.Handler {
	fsrv := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := strings.CutPrefix(r.URL.Path, prefix)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if isolate {
			w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
			w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
			w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
			w.Header().Set("Content-Security-Policy", CSP)
		}
		w.Header().Set("Cache-Control", "max-age=600")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + strings.TrimPrefix(p, "/")
		fsrv.ServeHTTP(w, r2)
	})
}

// Verify fetches base+build.json and every file it lists, until all match
// want (a local build's manifest) or the deadline passes. It returns how
// long it took.
func Verify(ctx context.Context, base string, want *Manifest, every time.Duration, log io.Writer) (time.Duration, error) {
	t0 := time.Now()
	base = strings.TrimSuffix(base, "/") + "/"
	for {
		bad, err := verifyOnce(ctx, base, want)
		if err == nil && bad == "" {
			return time.Since(t0), nil
		}
		if log != nil {
			fmt.Fprintf(log, "%s: not yet live (%v %s)\n", time.Since(t0).Round(time.Second), err, bad)
		}
		select {
		case <-ctx.Done():
			return time.Since(t0), fmt.Errorf("deployment never matched the build: %v %s", err, bad)
		case <-time.After(every):
		}
	}
}

func verifyOnce(ctx context.Context, base string, want *Manifest) (string, error) {
	keys := make([]string, 0, len(want.Files))
	for k := range want.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// Fetch each file the way the page does: index.html bare (it may be
		// up to max-age stale), every asset with ?v=<build>.
		u := base + k
		if k != "index.html" {
			u += "?v=" + want.Build
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return k, err
		}
		h := sha256.New()
		_, err = io.Copy(h, io.LimitReader(resp.Body, 200<<20))
		resp.Body.Close()
		if err != nil {
			return k, err
		}
		if resp.StatusCode != http.StatusOK {
			return k + " " + resp.Status, nil
		}
		if hex.EncodeToString(h.Sum(nil)) != want.Files[k] {
			return k + " differs", nil
		}
	}
	return "", nil
}
