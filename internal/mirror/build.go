// Package mirror writes the playground's image registry as static files: a
// read-only OCI distribution /v2 tree that GitHub Pages can serve, so the
// page pulls same-origin (ghcr.io sends no CORS headers, and the page's
// CSP allows connections to its own origin only).
//
// Layout under the output directory, for repository library/<name>:
//
//	v2/library/<name>/manifests/<tag>            the image index or manifest
//	v2/library/<name>/manifests/sha256:<hex>     the same, and every manifest, by digest
//	v2/library/<name>/blobs/sha256:<hex>         configs and layers
//
// A static server sends no Docker-Content-Digest and an octet-stream
// Content-Type; the engine's client sniffs the document and verifies every
// digest itself, so neither is needed.
package mirror

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-wasmcontainers/engine/image"
)

// Image is one image to publish.
type Image struct {
	Name string   // repository, under library/
	Tags []string // first one is the primary
	// Description is shown by the playground's help.
	Description string
	// Exactly one of Layer (a ready tar+gzip layer) or Files (built into a
	// tar+gzip layer) or Module (a CNCF wasm artifact) is set.
	Layer  []byte
	Files  map[string][]byte // path -> content; *.wasm get mode 0755
	Module []byte
	// Entrypoint, Cmd, Env and WorkingDir go into the config.
	Entrypoint []string
	Cmd        []string
	Env        []string
	WorkingDir string
	// Index wraps the manifest in an index with a decoy linux/amd64 entry.
	Index bool
	// Created is the config's creation time.
	Created time.Time
}

// Catalog entry, written to catalog.json for the page's help.
type CatalogEntry struct {
	Name        string   `json:"name"`
	Tags        []string `json:"tags"`
	Description string   `json:"description"`
	Digest      string   `json:"digest"`
}

type writer struct {
	dir  string
	repo string
}

func (w writer) put(kind, name string, b []byte) error {
	p := filepath.Join(w.dir, "v2", "library", w.repo, kind, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

func (w writer) blob(mt string, b []byte) (image.Descriptor, error) {
	d := image.FromBytes(b)
	return image.Descriptor{MediaType: mt, Digest: d, Size: int64(len(b))}, w.put("blobs", string(d), b)
}

func (w writer) manifest(mt string, b []byte) (image.Descriptor, error) {
	d := image.FromBytes(b)
	return image.Descriptor{MediaType: mt, Digest: d, Size: int64(len(b))}, w.put("manifests", string(d), b)
}

// TarGz builds a deterministic tar+gzip layer.
func TarGz(files map[string][]byte, mtime time.Time) ([]byte, error) {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	dirs := map[string]bool{}
	for _, n := range names {
		clean := strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+n)), "/")
		parts := strings.Split(clean, "/")
		for i := 1; i < len(parts); i++ {
			d := strings.Join(parts[:i], "/") + "/"
			if !dirs[d] {
				dirs[d] = true
				if err := tw.WriteHeader(&tar.Header{Name: d, Typeflag: tar.TypeDir, Mode: 0o755, ModTime: mtime}); err != nil {
					return nil, err
				}
			}
		}
		mode := int64(0o644)
		if strings.HasSuffix(clean, ".wasm") {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: clean, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(files[n])), ModTime: mtime}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(files[n]); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// Write publishes images under dir and writes dir/catalog.json.
func Write(dir string, images []Image) error {
	var catalog []CatalogEntry
	for _, img := range images {
		w := writer{dir: dir, repo: img.Name}
		created := img.Created
		if created.IsZero() {
			created = time.Unix(0, 0).UTC()
		}
		var m image.Manifest
		m.SchemaVersion = 2
		m.MediaType = image.MediaTypeOCIManifest
		switch {
		case img.Module != nil:
			l, err := w.blob(image.MediaTypeWasmLayer, img.Module)
			if err != nil {
				return err
			}
			l.Annotations = map[string]string{image.AnnotationTitle: img.Name + ".wasm"}
			cfg := map[string]any{"created": created.Format(time.RFC3339), "architecture": "wasm", "os": "wasip1",
				"layerDigests": []string{string(l.Digest)}}
			c, err := w.blob(image.MediaTypeWasmConfig, mustJSON(cfg))
			if err != nil {
				return err
			}
			m.Config, m.Layers = c, []image.Descriptor{l}
		default:
			layer := img.Layer
			if layer == nil {
				var err error
				if layer, err = TarGz(img.Files, created); err != nil {
					return err
				}
			}
			l, err := w.blob(image.MediaTypeOCILayerGz, layer)
			if err != nil {
				return err
			}
			diff, err := gunzipDigest(layer)
			if err != nil {
				return fmt.Errorf("%s: %w", img.Name, err)
			}
			cfg := image.Config{Created: created.Format(time.RFC3339), Architecture: "wasm", OS: "wasip1",
				Config: image.ExecConfig{Entrypoint: img.Entrypoint, Cmd: img.Cmd, Env: img.Env, WorkingDir: img.WorkingDir},
				RootFS: image.RootFS{Type: "layers", DiffIDs: []string{string(diff)}}}
			c, err := w.blob(image.MediaTypeOCIConfig, mustJSON(cfg))
			if err != nil {
				return err
			}
			m.Config, m.Layers = c, []image.Descriptor{l}
		}
		top, err := w.manifest(image.MediaTypeOCIManifest, mustJSON(m))
		if err != nil {
			return err
		}
		topBytes := mustJSON(m)
		if img.Index {
			decoyCfg, err := w.blob(image.MediaTypeOCIConfig, mustJSON(image.Config{Architecture: "amd64", OS: "linux", RootFS: image.RootFS{Type: "layers"}}))
			if err != nil {
				return err
			}
			decoy, err := w.manifest(image.MediaTypeOCIManifest, mustJSON(image.Manifest{SchemaVersion: 2, MediaType: image.MediaTypeOCIManifest, Config: decoyCfg, Layers: []image.Descriptor{}}))
			if err != nil {
				return err
			}
			decoy.Platform = &image.Platform{OS: "linux", Architecture: "amd64"}
			top.Platform = &image.WasmPlatform
			x := image.Index{SchemaVersion: 2, MediaType: image.MediaTypeOCIIndex, Manifests: []image.Descriptor{decoy, top}}
			topBytes = mustJSON(x)
			if top, err = w.manifest(image.MediaTypeOCIIndex, topBytes); err != nil {
				return err
			}
		}
		for _, tag := range img.Tags {
			if err := w.put("manifests", tag, topBytes); err != nil {
				return err
			}
		}
		catalog = append(catalog, CatalogEntry{Name: img.Name, Tags: img.Tags, Description: img.Description, Digest: string(top.Digest)})
	}
	return os.WriteFile(filepath.Join(dir, "catalog.json"), mustJSON(catalog), 0o644)
}

func gunzipDigest(layer []byte) (image.Digest, error) {
	zr, err := gzip.NewReader(bytes.NewReader(layer))
	if err != nil {
		return "", err
	}
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(zr); err != nil {
		return "", err
	}
	return image.FromBytes(raw.Bytes()), nil
}
