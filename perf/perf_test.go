//go:build perf

// Package perf measures container start natively, for comparison with the
// browser numbers of the e2e run: the engine's wazero executor (cold:
// compile + run; warm: cached compile) and wasmtime (`wasmtime run`, a
// process per run), on the same hello module. Run with
// go test -tags perf -v ./perf/ (wasmtime must be on PATH).
package perf

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/go-wasmcontainers/engine/rootfs"
	"github.com/go-wasmcontainers/engine/runtime"
	"github.com/go-wasmcontainers/engine/runtime/wazero"
)

func median(d []time.Duration) time.Duration {
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[len(d)/2]
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
