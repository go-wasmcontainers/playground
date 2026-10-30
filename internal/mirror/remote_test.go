package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-wasmcontainers/engine/image"
)

// TestWriteMirrored: a pinned image is published byte for byte under its
// digest and its tags, and a document that does not match its digest is
// refused rather than published.
func TestWriteMirrored(t *testing.T) {
	top := []byte(`{"schemaVersion":2}`)
	blob := []byte("layer")
	mi := &Mirrored{Top: top, Manifests: map[image.Digest][]byte{image.FromBytes(top): top},
		Blobs: map[image.Digest][]byte{image.FromBytes(blob): blob}}
	dir := t.TempDir()
	if err := Write(dir, []Image{{Name: "shell", Tags: []string{"0.1.0", "latest"}, Mirrored: mi}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"manifests/0.1.0", "manifests/latest", "manifests/" + string(image.FromBytes(top))} {
		if b, err := os.ReadFile(filepath.Join(dir, "v2", "library", "shell", p)); err != nil || string(b) != string(top) {
			t.Fatalf("%s: %q %v", p, b, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "v2", "library", "shell", "blobs", string(image.FromBytes(blob)))); err != nil || string(b) != "layer" {
		t.Fatalf("blob: %q %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "catalog.json")); !strings.Contains(string(b), string(mi.Digest())) {
		t.Fatalf("catalog: %s", b)
	}

	bad := &Mirrored{Top: top, Manifests: map[image.Digest][]byte{image.FromBytes(top): top},
		Blobs: map[image.Digest][]byte{image.FromBytes(blob): []byte("other")}}
	if err := Write(t.TempDir(), []Image{{Name: "shell", Tags: []string{"latest"}, Mirrored: bad}}); err == nil {
		t.Fatal("published a blob that does not match its digest")
	}
}

// TestPinnedOnGHCR fetches the pinned images (network: PKGX_NET=1): the
// registry still serves the pinned digest, with a wasip1 manifest.
func TestPinnedOnGHCR(t *testing.T) {
	if os.Getenv("PKGX_NET") == "" {
		t.Skip("set PKGX_NET=1 to fetch from ghcr.io")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, p := range Remote {
		mi, err := FetchPinned(ctx, p, 100<<20)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(p.Ref, "@"+string(mi.Digest())) || len(mi.Blobs) < 2 {
			t.Fatalf("%s: %s, %d blobs", p.Name, mi.Digest(), len(mi.Blobs))
		}
	}
	if _, err := FetchPinned(ctx, Pinned{Name: "x", Ref: "ghcr.io/go-wasmcontainers/shell:0.1.0"}, 1<<20); err == nil {
		t.Fatal("fetched an unpinned reference")
	}
}
