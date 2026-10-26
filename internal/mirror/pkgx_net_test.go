package mirror

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestBottlesOnGHCR fetches the pilots from ghcr.io (network: set
// PKGX_NET=1) and lists what each bottle holds, which is how one finds
// whether a library bottle (zlib.net) ships a command.
func TestBottlesOnGHCR(t *testing.T) {
	if os.Getenv("PKGX_NET") == "" {
		t.Skip("set PKGX_NET=1 to fetch from ghcr.io")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, b := range append(Pilots, Bottle{Name: "zlib", Ref: "ghcr.io/go-pkgx/packages/zlib.net:1.3.2", Binary: "minigzip"}) {
		bl, err := FetchBottle(ctx, b, 100<<20)
		if bl != nil {
			var bins []string
			for _, e := range bl.Entries {
				if strings.Contains(e, "/bin/") || strings.HasPrefix(e, "bin/") {
					bins = append(bins, e)
				}
			}
			t.Logf("%s: %d entries, %d bytes; bin: %v; wasm at %q", b.Ref, len(bl.Entries), len(bl.Layer), bins, bl.WasmPath)
		}
		if err != nil {
			t.Logf("%s: %v", b.Ref, err)
		}
	}
}
