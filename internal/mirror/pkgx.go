package mirror

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/go-wasmcontainers/engine/image"
)

// BottleLayer is what FetchBottle found.
type BottleLayer struct {
	Ref       image.Reference
	Layer     []byte // the bottle tarball, verified against its digest
	MediaType string
	// Zstd says the tarball is zstd (the C bottles), not gzip.
	Zstd    bool
	Entries []string // tar entry names
	// Wasm maps a binary name to its module's path ("/bin/lua.wasm" for
	// "lua"): the regular files named bin/<name>.wasm, whatever prefix the
	// bottle puts before bin/.
	Wasm map[string]string
}

// FetchBottle pulls the wasip1/wasm bottle at ref ("ghcr.io/go-pkgx/packages/
// <project>:<version>") from its registry, verifying every document and blob
// against its digest, bounds the layer (compressed and unpacked), checks the
// tar's paths, and finds its modules. The layer is untrusted input.
func FetchBottle(ctx context.Context, refStr string, lim Limits) (*BottleLayer, error) {
	ref, err := image.ParseReference(refStr)
	if err != nil {
		return nil, err
	}
	il := image.DefaultLimits
	src := image.NewRegistrySource(ref, image.RegistryOptions{UserAgent: "go-wasmcontainers-playground-mirror"}, il)
	top, err := src.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	manDesc := top
	doc := top.Data
	if image.Sniff(top.MediaType, doc) == image.KindIndex {
		x, err := image.ParseIndex(doc, il)
		if err != nil {
			return nil, err
		}
		manDesc, err = image.SelectManifest(x, image.WasmPlatform)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", refStr, err)
		}
		doc, err = fetchVerified(ctx, src, manDesc, il.MaxManifestBytes)
		if err != nil {
			return nil, err
		}
	}
	m, err := image.ParseManifest(doc, il)
	if err != nil {
		return nil, err
	}
	if len(m.Layers) != 1 {
		return nil, fmt.Errorf("%s: %d layers, a bottle has one", refStr, len(m.Layers))
	}
	l := m.Layers[0]
	if l.Size > lim.MaxLayer {
		return nil, &LimitError{fmt.Sprintf("%s: layer of %d bytes, limit %d", refStr, l.Size, lim.MaxLayer)}
	}
	zst := strings.HasSuffix(l.MediaType, "+zstd")
	if !zst && !strings.HasSuffix(l.MediaType, "+gzip") && !strings.HasSuffix(l.MediaType, ".gzip") {
		return nil, fmt.Errorf("%s: layer media type %q is neither gzip nor zstd", refStr, l.MediaType)
	}
	data, err := fetchVerified(ctx, src, l, lim.MaxLayer)
	if err != nil {
		return nil, err
	}
	out := &BottleLayer{Ref: ref, Layer: data, MediaType: l.MediaType, Zstd: zst}
	zr, closeFn, err := decompress(data, zst, lim.MaxUnpacked)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	out.Entries, out.Wasm, err = ScanTar(zr, lim)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", refStr, err)
	}
	return out, nil
}

// LimitError is a bottle that exceeds a limit: a finding about the bottle,
// not a failure of the mirror.
type LimitError struct{ Msg string }

func (e *LimitError) Error() string { return e.Msg }

// UnsafeError is a bottle whose tar holds a path or link that escapes.
type UnsafeError struct{ Msg string }

func (e *UnsafeError) Error() string { return e.Msg }

// ScanTar reads an uncompressed tar and returns its entry names and the
// modules it holds. It never trusts the archive:
//
//   - at most lim.MaxUnpacked bytes are read in all (an error, not a
//     truncation: a bomb must not pass as a short archive) and lim.MaxEntries
//     entries are listed;
//   - an absolute name, a name with a ".." segment or a NUL, and a symlink or
//     hard link whose target is absolute or leaves the root, are refused.
func ScanTar(r io.Reader, lim Limits) (entries []string, wasm map[string]string, err error) {
	cr := &cappedReader{r: r, left: lim.MaxUnpacked}
	tr := tar.NewReader(cr)
	wasm = map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			var le *LimitError
			if errors.As(err, &le) {
				return nil, nil, err
			}
			if cr.over {
				return nil, nil, &LimitError{fmt.Sprintf("unpacked size over %d bytes", lim.MaxUnpacked)}
			}
			return nil, nil, err
		}
		if len(entries) >= lim.MaxEntries {
			return nil, nil, &LimitError{fmt.Sprintf("more than %d tar entries", lim.MaxEntries)}
		}
		if err := safeName(h.Name); err != nil {
			return nil, nil, err
		}
		if h.Typeflag == tar.TypeSymlink || h.Typeflag == tar.TypeLink {
			if err := safeLink(h.Name, h.Linkname, h.Typeflag == tar.TypeLink); err != nil {
				return nil, nil, err
			}
		}
		entries = append(entries, h.Name)
		clean := strings.TrimPrefix(path.Clean("/"+h.Name), "/")
		if h.Typeflag == tar.TypeReg && strings.HasSuffix(clean, ".wasm") && path.Base(path.Dir(clean)) == "bin" {
			b := strings.TrimSuffix(path.Base(clean), ".wasm")
			if _, dup := wasm[b]; !dup {
				wasm[b] = "/" + clean
			}
		}
	}
	if cr.over {
		return nil, nil, &LimitError{fmt.Sprintf("unpacked size over %d bytes", lim.MaxUnpacked)}
	}
	sort.Strings(entries)
	return entries, wasm, nil
}

func safeName(n string) error {
	if n == "" || strings.ContainsRune(n, 0) {
		return &UnsafeError{fmt.Sprintf("tar entry name %q", n)}
	}
	if strings.HasPrefix(n, "/") {
		return &UnsafeError{fmt.Sprintf("tar entry %q has an absolute path", n)}
	}
	for _, s := range strings.Split(n, "/") {
		if s == ".." {
			return &UnsafeError{fmt.Sprintf("tar entry %q has a .. segment", n)}
		}
	}
	return nil
}

// safeLink refuses a link target that is absolute or climbs out of the root.
// A symlink's target is relative to the link's directory, a hard link's to the
// root.
func safeLink(name, target string, hard bool) error {
	if target == "" || strings.ContainsRune(target, 0) || strings.HasPrefix(target, "/") {
		return &UnsafeError{fmt.Sprintf("tar entry %q links to %q", name, target)}
	}
	base := ""
	if !hard {
		base = path.Dir(name)
	}
	joined := path.Join(base, target)
	if joined == ".." || strings.HasPrefix(joined, "../") {
		return &UnsafeError{fmt.Sprintf("tar entry %q links to %q, outside the root", name, target)}
	}
	return nil
}

// cappedReader fails once more than left bytes were requested through it.
type cappedReader struct {
	r    io.Reader
	left int64
	over bool
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		// Probe for one more byte: an archive of exactly the limit is fine.
		var b [1]byte
		n, err := c.r.Read(b[:])
		if n > 0 {
			c.over = true
			return 0, &LimitError{"unpacked size over the limit"}
		}
		return 0, err
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

func fetchVerified(ctx context.Context, src image.Source, d image.Descriptor, max int64) ([]byte, error) {
	if d.Size > max {
		return nil, &LimitError{fmt.Sprintf("%s is %d bytes, limit %d", d.Digest, d.Size, max)}
	}
	rc, err := src.Fetch(ctx, d)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(image.NewVerifyingReader(rc, d.Digest, d.Size))
}
