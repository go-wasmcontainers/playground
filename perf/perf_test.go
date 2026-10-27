//go:build perf

// Package perf measures container start natively, for comparison with the
// browser numbers of the e2e run: the engine's wazero executor (cold:
// compile + run; warm: cached compile) and wasmtime (`wasmtime run`, a
// process per run), on the same hello module. Run with
// go test -tags perf -v ./perf/ (wasmtime must be on PATH).
package perf

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-wasmcontainers/engine/netdriver"
	"github.com/go-wasmcontainers/engine/rootfs"
	"github.com/go-wasmcontainers/engine/runtime"
	"github.com/go-wasmcontainers/engine/runtime/wazero"
)

func median(d []time.Duration) time.Duration {
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[len(d)/2]
}

type buf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *buf) Write(p []byte) (int, error) { b.mu.Lock(); defer b.mu.Unlock(); return b.b.Write(p) }
func (b *buf) String() string              { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

// TestNetBench runs the playground's `net bench` (200 sequential GETs and
// one 8 MiB GET between two containers on an in-engine network) natively
// on the engine (wazero, the engine's WASI: the browser's socket code), and
// the same pattern with plain Go net/http over the host's loopback TCP, the
// upper bound. The browser numbers are in the e2e report (httpP50Ms…).
func TestNetBench(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "net.wasm")
	b := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", bin, "../images/net")
	b.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if msg, err := b.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, msg)
	}
	mod, _ := os.ReadFile(bin)
	img := rootfs.New()
	img.WriteFile("/net.wasm", mod, 0o755)
	img.Freeze()
	ctx := context.Background()
	n, _ := netdriver.NewLocal(netdriver.DefaultPrefix)
	defer n.Close()
	e := wazero.New(wazero.Options{EngineWASI: true})
	defer e.Close(ctx)
	start := func(name string, out *buf, args ...string) runtime.Process {
		ep, err := n.Attach(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		p, err := e.Start(ctx, runtime.Spec{FS: img.Clone(rootfs.Quota{}), Path: "/net.wasm", Args: append([]string{"net"}, args...),
			Net: netdriver.WithDNS(ep), Stdout: out, Stderr: out})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	var srvOut, out buf
	srv := start("web", &srvOut, "serve")
	defer func() { srv.Kill(); srv.Wait() }()
	if r := start("client", &out, "bench", "http://web", "200").Wait(); r.ExitCode != 0 {
		t.Fatalf("%+v %s", r, out.String())
	}
	t.Logf("perf_test net bench, engine on wazero (in-engine network): %s", strings.TrimSpace(out.String()))

	// Plain Go over loopback TCP, the same requests.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 64<<10)
	go http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bulk" {
			k := 8 << 20
			w.Header().Set("Content-Length", strconv.Itoa(k))
			for k > 0 {
				w.Write(chunk[:min(k, len(chunk))])
				k -= len(chunk)
			}
			return
		}
		io.WriteString(w, "hello\n")
	}))
	defer l.Close()
	c := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1}}
	base := "http://" + l.Addr().String()
	var lat []time.Duration
	for i := 0; i < 200; i++ {
		t0 := time.Now()
		resp, err := c.Get(base + "/bench")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		lat = append(lat, time.Since(t0))
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	t0 := time.Now()
	resp, err := c.Get(base + "/bulk")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	el := time.Since(t0)
	t.Logf("perf_test net bench, native Go over loopback TCP: p50 %.2f ms, p90 %.2f ms; %.1f MB/s",
		float64(lat[100].Microseconds())/1000, float64(lat[180].Microseconds())/1000, float64(got)/el.Seconds()/1e6)
}

func TestStartTimes(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "hello.wasm")
	b := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", bin, "../images/hello")
	b.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if msg, err := b.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, msg)
	}
	mod, _ := os.ReadFile(bin)
	fs := rootfs.New()
	fs.WriteFile("/hello.wasm", mod, 0o755)
	fs.Freeze()
	ctx := context.Background()
	const n = 5
	run := func(e *wazero.Executor) time.Duration {
		t0 := time.Now()
		p, err := e.Start(ctx, runtime.Spec{FS: fs.Clone(rootfs.Quota{}), Path: "/hello.wasm"})
		if err != nil {
			t.Fatal(err)
		}
		if r := p.Wait(); r.ExitCode != 0 {
			t.Fatal(r)
		}
		return time.Since(t0)
	}
	for _, engineWASI := range []bool{false, true} {
		var cold, warm []time.Duration
		for i := 0; i < n; i++ {
			e := wazero.New(wazero.Options{EngineWASI: engineWASI})
			cold = append(cold, run(e))
			warm = append(warm, run(e))
			e.Close(ctx)
		}
		host := "wazero's WASI"
		if engineWASI {
			host = "the engine's WASI (as in the browser)"
		}
		t.Logf("perf_test wazero, %s: cold %v, warm %v (median of %d)", host, median(cold).Round(time.Millisecond), median(warm).Round(100*time.Microsecond), n)
	}
	if _, err := exec.LookPath("wasmtime"); err != nil {
		t.Logf("perf_test wasmtime: not installed, skipped")
		return
	}
	var wt []time.Duration
	for i := 0; i < n; i++ {
		t0 := time.Now()
		out, err := exec.Command("wasmtime", "run", bin).CombinedOutput()
		if err != nil || string(out) != "Hello from a WebAssembly container!\n" {
			t.Fatalf("%v %q", err, out)
		}
		wt = append(wt, time.Since(t0))
	}
	v, _ := exec.Command("wasmtime", "--version").Output()
	t.Logf("perf_test %s: `wasmtime run hello.wasm` %v (median of %d, process start + compile + run)", string(v[:len(v)-1]), median(wt).Round(time.Millisecond), n)
}
