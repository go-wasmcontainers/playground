package mirror

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/go-wasmcontainers/engine/image"
)

// Bottle names a pkgx bottle published for wasip1/wasm.
type Bottle struct {
	// Name is the local image name ("age").
	Name string
	// Ref is the registry reference ("ghcr.io/go-pkgx/packages/filippo.io/age:1.3.2").
	Ref string
	// Binary is the program inside the bottle ("age" for bin/age.wasm).
	Binary string
}

// Pilots are the pkgx wasip1 pilots the playground carries.
var Pilots = []Bottle{
	{Name: "age", Ref: "ghcr.io/go-pkgx/packages/filippo.io/age:1.3.2", Binary: "age"},
	{Name: "go-md2man", Ref: "ghcr.io/go-pkgx/packages/github.com/cpuguy83/go-md2man:2.0.7", Binary: "go-md2man"},
	{Name: "shfmt", Ref: "ghcr.io/go-pkgx/packages/mvdan.cc/sh:3.14.1", Binary: "shfmt"},
	// C bottles: lua raises errors with WebAssembly exception handling.
	{Name: "lua", Ref: "ghcr.io/go-pkgx/packages/lua.org:5.4.9", Binary: "lua"},
	{Name: "sqlite3", Ref: "ghcr.io/go-pkgx/packages/sqlite.org:3.53.4", Binary: "sqlite3"},
	// zlib's bottle is a library plus its minigzip example command.
	{Name: "minigzip", Ref: "ghcr.io/go-pkgx/packages/zlib.net:1.3.2", Binary: "minigzip"},
}

// BottleLayer is what FetchBottle found.
type BottleLayer struct {
	Ref       image.Reference
	Layer     []byte // the bottle tarball, verified against its digest
	MediaType string
	// Zstd says the tarball is zstd (the C bottles), not gzip.
	Zstd    bool
	Entries []string // tar entry names
	// WasmPath is the entry that is bin/<Binary>.wasm, whatever prefix the
	// bottle puts before bin/.
	WasmPath string
}

// FetchBottle pulls the wasip1/wasm bottle of b from its registry,
// verifying every document and blob, and finds its module.
func FetchBottle(ctx context.Context, b Bottle, maxLayer int64) (*BottleLayer, error) {
	ref, err := image.ParseReference(b.Ref)
	if err != nil {
		return nil, err
	}
	lim := image.DefaultLimits
	src := image.NewRegistrySource(ref, image.RegistryOptions{UserAgent: "go-wasmcontainers-playground-mirror"}, lim)
	top, err := src.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	manDesc := top
	doc := top.Data
	if image.Sniff(top.MediaType, doc) == image.KindIndex {
		x, err := image.ParseIndex(doc, lim)
		if err != nil {
			return nil, err
		}
		manDesc, err = image.SelectManifest(x, image.WasmPlatform)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", b.Ref, err)
		}
		doc, err = fetchVerified(ctx, src, manDesc, lim.MaxManifestBytes)
		if err != nil {
			return nil, err
		}
	}
	m, err := image.ParseManifest(doc, lim)
	if err != nil {
		return nil, err
	}
	if len(m.Layers) != 1 {
		return nil, fmt.Errorf("%s: %d layers, a bottle has one", b.Ref, len(m.Layers))
	}
	l := m.Layers[0]
	if l.Size > maxLayer {
		return nil, fmt.Errorf("%s: layer of %d bytes, limit %d", b.Ref, l.Size, maxLayer)
	}
	zst := strings.HasSuffix(l.MediaType, "+zstd")
	if !zst && !strings.HasSuffix(l.MediaType, "+gzip") && !strings.HasSuffix(l.MediaType, ".gzip") {
		return nil, fmt.Errorf("%s: layer media type %q is neither gzip nor zstd", b.Ref, l.MediaType)
	}
	data, err := fetchVerified(ctx, src, l, maxLayer)
	if err != nil {
		return nil, err
	}
	out := &BottleLayer{Ref: ref, Layer: data, MediaType: l.MediaType, Zstd: zst}
	zr, closeFn, err := decompress(data, zst)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		out.Entries = append(out.Entries, h.Name)
		clean := strings.TrimPrefix(path.Clean("/"+h.Name), "/")
		if path.Base(clean) == b.Binary+".wasm" && path.Base(path.Dir(clean)) == "bin" {
			out.WasmPath = "/" + clean
		}
	}
	sort.Strings(out.Entries)
	if out.WasmPath == "" {
		return out, fmt.Errorf("%s: no bin/%s.wasm in the bottle", b.Ref, b.Binary)
	}
	return out, nil
}

func fetchVerified(ctx context.Context, src image.Source, d image.Descriptor, max int64) ([]byte, error) {
	if d.Size > max {
		return nil, fmt.Errorf("%s is %d bytes, limit %d", d.Digest, d.Size, max)
	}
	rc, err := src.Fetch(ctx, d)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(image.NewVerifyingReader(rc, d.Digest, d.Size))
}
