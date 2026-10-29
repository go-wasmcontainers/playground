package mirror

import (
	"context"
	"fmt"

	"github.com/go-wasmcontainers/engine/image"
)

// Pinned is an image the playground mirrors verbatim from a registry,
// pinned by digest: the page pulls exactly the bytes that were reviewed.
type Pinned struct {
	// Name is the local image name ("shell").
	Name string
	// Ref is the registry reference with its digest
	// ("ghcr.io/go-wasmcontainers/shell:0.1.0@sha256:…").
	Ref string
	// Tags are the mirror's tags for it (the first is the primary).
	Tags        []string
	Description string
}

// Remote are the pinned images the playground carries.
var Remote = []Pinned{
	{Name: "shell", Ref: "ghcr.io/go-wasmcontainers/shell:0.1.0@" + shellDigest, Tags: []string{"0.1.0", "latest"},
		Description: "a shell and the usual commands (go-wasmcontainers/shell 0.1.0): wctr run --rm -it shell"},
}

// shellDigest is ghcr.io/go-wasmcontainers/shell:0.1.0, as `site pin`
// printed it.
const shellDigest = "sha256:79b185bfd505b523227396208976641e616723c3238c2a2edf18adcf15423b0e"

// Mirrored is a pinned image's content: its top document (an index or a
// manifest) as published, and the wasip1/wasm manifest, config and layers
// it leads to. Everything was verified against its digest.
type Mirrored struct {
	Top          []byte
	TopMediaType string
	// Manifests and Blobs by digest (the top document is in Manifests too).
	Manifests map[image.Digest][]byte
	Blobs     map[image.Digest][]byte
}

// Digest is the top document's digest: the pin.
func (m *Mirrored) Digest() image.Digest { return image.FromBytes(m.Top) }

// Resolve returns the digest of the document a registry serves for ref.
func Resolve(ctx context.Context, ref string) (image.Digest, error) {
	r, err := image.ParseReference(ref)
	if err != nil {
		return "", err
	}
	top, err := image.NewRegistrySource(r, image.RegistryOptions{UserAgent: "go-wasmcontainers-playground-mirror"}, image.DefaultLimits).Resolve(ctx)
	if err != nil {
		return "", err
	}
	return top.Digest, nil
}

// FetchPinned pulls p from its registry. The reference must carry a
// digest, and the registry must serve exactly that document; the
// wasip1/wasm manifest, its config and its layers are verified too. No
// blob may exceed maxBlob bytes.
func FetchPinned(ctx context.Context, p Pinned, maxBlob int64) (*Mirrored, error) {
	ref, err := image.ParseReference(p.Ref)
	if err != nil {
		return nil, err
	}
	if ref.Digest == "" {
		return nil, fmt.Errorf("%s: not pinned by digest", p.Ref)
	}
	lim := image.DefaultLimits
	src := image.NewRegistrySource(ref, image.RegistryOptions{UserAgent: "go-wasmcontainers-playground-mirror"}, lim)
	top, err := src.Resolve(ctx) // checks the digest
	if err != nil {
		return nil, err
	}
	if top.Data == nil {
		if top.Data, err = fetchVerified(ctx, src, top, lim.MaxManifestBytes); err != nil {
			return nil, err
		}
	}
	if top.Digest != ref.Digest || image.FromBytes(top.Data) != ref.Digest {
		return nil, fmt.Errorf("%s: resolved %s", p.Ref, top.Digest)
	}
	out := &Mirrored{Top: top.Data, TopMediaType: top.MediaType,
		Manifests: map[image.Digest][]byte{top.Digest: top.Data}, Blobs: map[image.Digest][]byte{}}
	doc := top.Data
	if image.Sniff(top.MediaType, doc) == image.KindIndex {
		x, err := image.ParseIndex(doc, lim)
		if err != nil {
			return nil, err
		}
		md, err := image.SelectManifest(x, image.WasmPlatform)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.Ref, err)
		}
		if doc, err = fetchVerified(ctx, src, md, lim.MaxManifestBytes); err != nil {
			return nil, err
		}
		out.Manifests[md.Digest] = doc
	}
	m, err := image.ParseManifest(doc, lim)
	if err != nil {
		return nil, err
	}
	for _, d := range append([]image.Descriptor{m.Config}, m.Layers...) {
		if d.Size > maxBlob {
			return nil, fmt.Errorf("%s: blob %s of %d bytes, limit %d", p.Ref, d.Digest, d.Size, maxBlob)
		}
		b, err := fetchVerified(ctx, src, d, maxBlob)
		if err != nil {
			return nil, err
		}
		out.Blobs[d.Digest] = b
	}
	return out, nil
}
