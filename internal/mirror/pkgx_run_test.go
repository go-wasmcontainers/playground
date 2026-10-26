package mirror

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-wasmcontainers/engine/rootfs"
	"github.com/go-wasmcontainers/engine/runtime"
	"github.com/go-wasmcontainers/engine/runtime/wazero"
)

// TestBottlesRunNatively runs the C bottles with the engine's native
// executor (wazero), with its own WASI and with the engine's: lua raises
// errors with WebAssembly exception handling, so this is where an
// unsupported proposal would show (network: PKGX_NET=1).
func TestBottlesRunNatively(t *testing.T) {
	if os.Getenv("PKGX_NET") == "" {
		t.Skip("set PKGX_NET=1 to fetch from ghcr.io")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	byName := map[string]Bottle{}
	for _, b := range Pilots {
		byName[b.Name] = b
	}
	for _, c := range []struct {
		bottle string
		args   []string
		stdin  string
		want   string
	}{
		{"lua", []string{"lua", "-e", "print(pcall(error))"}, "", "false\tnil"},
		{"lua", []string{"lua", "-e", "print(pcall(function() error('boom') end))"}, "", "boom"},
		{"sqlite3", []string{"sqlite3", ":memory:", "select 6*7;"}, "", "42"},
		{"minigzip", []string{"minigzip"}, "hello", "\x1f\x8b"},
	} {
		for _, engineWASI := range []bool{false, true} {
			b := byName[c.bottle]
			bl, err := FetchBottle(ctx, b, 100<<20)
			if err != nil {
				t.Fatal(err)
			}
			mt := "application/vnd.oci.image.layer.v1.tar+gzip"
			if bl.Zstd {
				mt = "application/vnd.oci.image.layer.v1.tar+zstd"
			}
			fs, err := rootfs.Build([]rootfs.LayerReader{{MediaType: mt, Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(bl.Layer)), nil }}}, rootfs.DefaultLimits)
			if err != nil {
				t.Fatalf("%s: %v", c.bottle, err)
			}
			e := wazero.New(wazero.Options{EngineWASI: engineWASI})
			var out, errb bytes.Buffer
			p, err := e.Start(ctx, runtime.Spec{FS: fs.Clone(rootfs.Quota{}), Path: bl.WasmPath, Args: c.args,
				Env: []string{"PWD=/"}, Stdin: strings.NewReader(c.stdin), Stdout: &out, Stderr: &errb})
			if err != nil {
				t.Errorf("%s (engine WASI %v): start: %v", c.bottle, engineWASI, err)
				e.Close(ctx)
				continue
			}
			r := p.Wait()
			e.Close(ctx)
			t.Logf("%s (engine WASI %v) %q: exit %d %s; stdout %q; stderr %q", c.bottle, engineWASI, c.args, r.ExitCode, r.Error, out.String(), errb.String())
			if !strings.Contains(out.String(), c.want) {
				t.Errorf("%s (engine WASI %v): stdout %q lacks %q", c.bottle, engineWASI, out.String(), c.want)
			}
		}
	}
}
