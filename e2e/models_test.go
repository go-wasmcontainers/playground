//go:build e2e

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/go-wasmcontainers/playground/internal/site"
)

// A model stand-in: a local server that serves the "pinned" bytes of a test
// model (a synthetic 20 MiB file, registered through ?modelstub= on a loopback
// page), so CI never reaches Hugging Face. The engine checks its size and
// sha256 exactly as for a real model.
type stub struct {
	data    []byte
	hits    atomic.Int64 // requests for the model
	corrupt atomic.Bool  // serve one flipped byte
	down    atomic.Bool  // answer 503
}

func (s *stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.hits.Add(1)
	if s.down.Load() {
		http.Error(w, "stand-in is down", http.StatusServiceUnavailable)
		return
	}
	d := s.data
	if s.corrupt.Load() {
		d = append([]byte(nil), d...)
		d[len(d)/2] ^= 0x01
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	http.ServeContent(w, r, "model.bin", time.Time{}, strings.NewReader(string(d)))
}

func newStub() *stub {
	r := rand.New(rand.NewPCG(7, 11))
	b := make([]byte, 20<<20)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return &stub{data: b}
}

func serveWithStub(t *testing.T, st *stub) string {
	t.Helper()
	_, dir := serveCopy(t, true)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/stub/model.bin", st)
	mux.Handle("/", site.Handler(dir, "/playground", true))
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return "http://" + l.Addr().String() + "/playground/"
}

func TestModelDownload(t *testing.T) {
	if os.Getenv("E2E_URL") != "" {
		t.Skip("E2E_URL set: TestLive only")
	}
	pw, err := playwright.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Stop()
	for _, name := range []string{"chromium", "firefox"} {
		if os.Getenv("E2E_BROWSERS") != "" && !strings.Contains(os.Getenv("E2E_BROWSERS"), name) {
			continue
		}
		t.Run(name, func(t *testing.T) { modelDownload(t, pw, name) })
	}
}

func modelDownload(t *testing.T, pw *playwright.Playwright, browserName string) {
	st := newStub()
	sum := sha256.Sum256(st.data)
	spec := fmt.Sprintf("stubm,%d,%s,/stub/model.bin", len(st.data), hex.EncodeToString(sum[:]))
	base := serveWithStub(t, st)
	bt := pw.Chromium
	if browserName == "firefox" {
		bt = pw.Firefox
	}
	browser, err := bt.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true), Timeout: playwright.Float(180000)})
	if err != nil {
		t.Fatalf("launch %s: %v", browserName, err)
	}
	defer browser.Close()
	ctx, err := browser.NewContext() // a fresh profile: empty OPFS
	if err != nil {
		t.Fatal(err)
	}
	page, err := ctx.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	b := tab{t, page, browserName}
	modelSteps(t, b, st, func(u string) error { _, err := page.Goto(u); return err },
		func() error { _, err := page.Reload(); return err }, base+"?ui=terminal&modelstub="+url.QueryEscape(spec))
}

// modelSteps is the model download scenario, on any browser: open opens the
// page, reload reloads it.
func modelSteps(t *testing.T, b tab, st *stub, open func(url string) error, reload func() error, pageURL string) {
	t.Helper()
	if err := open(pageURL); err != nil {
		t.Fatal(err)
	}
	b.ready()
	b.must("wctr pull lab", 0, "Downloaded newer image for lab:latest")

	// First run: downloads with progress, verifies, stores, mounts.
	b.must("models", 0, "stubm", "not downloaded")
	b.must("wctr run --rm --model stubm lab ls /models", 0, "downloading stubm", "100%", "stubm.gguf")
	if st.hits.Load() == 0 {
		t.Fatal("the first run fetched nothing")
	}
	b.must("models", 0, "stubm", "cached")

	// Second run: from OPFS, no network.
	st.hits.Store(0)
	b.must("wctr run --rm --model stubm lab ls /models", 0, "stubm.gguf")
	if n := st.hits.Load(); n != 0 {
		t.Fatalf("the second run made %d requests", n)
	}
	// Across a reload too (a new engine worker re-verifies the cached file).
	if err := reload(); err != nil {
		t.Fatal(err)
	}
	b.ready()
	b.must("wctr run --rm --model stubm lab ls /models", 0, "stubm.gguf")
	if n := st.hits.Load(); n != 0 {
		t.Fatalf("after a reload: %d requests", n)
	}

	// Removal.
	b.must("models rm stubm", 0, "removed stubm", "MB freed")
	b.must("models", 0, "not downloaded")

	// A corrupted byte (same size) is refused by the sha256 check and
	// leaves nothing behind.
	st.corrupt.Store(true)
	if c, out := b.run("wctr run --rm --model stubm lab ls /models"); c == 0 || !strings.Contains(out, "sha256") {
		t.Fatalf("corrupted model: exit %d\n%s", c, out)
	}
	b.must("models", 0, "not downloaded")
	st.corrupt.Store(false)

	// Offline (the stand-in answers 503): a clear error, no partial model.
	st.down.Store(true)
	if c, out := b.run("wctr run --rm --model stubm lab ls /models"); c == 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("offline: exit %d\n%s", c, out)
	}
	b.must("models", 0, "not downloaded")
	st.down.Store(false)

	// Back online: it works again.
	b.must("wctr run --rm --model stubm lab ls /models", 0, "stubm.gguf")

	// The real thing, only on request: Hugging Face, then the llm image.
	if os.Getenv("LLM_LIVE_HF") == "1" {
		b.must("wctr pull llm", 0)
		b.must("wctr run --rm --gpu --gpu-memory 512m --model smollm2-135m-instruct-q4_0 llm -n 8 \"Once upon a time\"", 0, "text:")
	}
}
