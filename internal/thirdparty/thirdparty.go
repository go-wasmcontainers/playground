// Package thirdparty fetches the page's third-party scripts from the npm
// registry, checks them against the registry's published integrity hash,
// and writes the files the page serves itself (its CSP allows no other
// origin). The output is committed; this runs only to update it.
package thirdparty

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Package is one npm package and the files taken from it.
type Package struct {
	Name, Version string
	// Files maps a path inside the tarball (under package/) to the output
	// file name.
	Files map[string]string
}

// Packages are the page's dependencies, pinned.
var Packages = []Package{
	{Name: "@xterm/xterm", Version: "5.5.0", Files: map[string]string{
		"lib/xterm.js": "xterm.js", "css/xterm.css": "xterm.css", "LICENSE": "xterm.LICENSE",
	}},
	{Name: "@xterm/addon-fit", Version: "0.10.0", Files: map[string]string{
		"lib/addon-fit.js": "addon-fit.js", "LICENSE": "addon-fit.LICENSE",
	}},
}

func get(ctx context.Context, url string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s: more than %d bytes", url, max)
	}
	return b, err
}

// tarball fetches an npm package tarball and checks it against the
// registry's sha512 integrity.
func tarball(ctx context.Context, name, version string) ([]byte, error) {
	meta, err := get(ctx, "https://registry.npmjs.org/"+name+"/"+version, 4<<20)
	if err != nil {
		return nil, err
	}
	var m struct {
		Dist struct {
			Tarball   string `json:"tarball"`
			Integrity string `json:"integrity"`
		} `json:"dist"`
	}
	if err := json.Unmarshal(meta, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	alg, want, ok := strings.Cut(m.Dist.Integrity, "-")
	if !ok || alg != "sha512" {
		return nil, fmt.Errorf("%s: integrity %q is not sha512", name, m.Dist.Integrity)
	}
	if !strings.HasPrefix(m.Dist.Tarball, "https://registry.npmjs.org/") {
		return nil, fmt.Errorf("%s: tarball %q is off the registry", name, m.Dist.Tarball)
	}
	tgz, err := get(ctx, m.Dist.Tarball, 64<<20)
	if err != nil {
		return nil, err
	}
	sum := sha512.Sum512(tgz)
	if base64.StdEncoding.EncodeToString(sum[:]) != want {
		return nil, fmt.Errorf("%s: tarball does not match its integrity hash", name)
	}
	return tgz, nil
}

// PlaywrightDriver assembles the driver directory playwright-go runs
// (package/cli.js = npm playwright-core) without the driver zips its
// download step fetches, which the Playwright CDN no longer serves. Node
// comes from PLAYWRIGHT_NODEJS_PATH.
func PlaywrightDriver(ctx context.Context, dir, version string) error {
	tgz, err := tarball(ctx, "playwright-core", version)
	if err != nil {
		return err
	}
	zr, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		rel, ok := strings.CutPrefix(h.Name, "package/")
		if !ok || h.Typeflag != tar.TypeReg || strings.Contains(rel, "..") || filepath.IsAbs(rel) {
			continue
		}
		out := filepath.Join(dir, "package", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(tr, 64<<20))
		if err != nil {
			return err
		}
		if err := os.WriteFile(out, b, 0o644); err != nil {
			return err
		}
	}
}

// Fetch downloads, verifies and extracts every package into dir.
func Fetch(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, p := range Packages {
		tgz, err := tarball(ctx, p.Name, p.Version)
		if err != nil {
			return err
		}
		zr, err := gzip.NewReader(bytes.NewReader(tgz))
		if err != nil {
			return err
		}
		tr := tar.NewReader(zr)
		found := 0
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			out, ok := p.Files[strings.TrimPrefix(h.Name, "package/")]
			if !ok || h.Typeflag != tar.TypeReg {
				continue
			}
			b, err := io.ReadAll(io.LimitReader(tr, 8<<20))
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, out), b, 0o644); err != nil {
				return err
			}
			found++
		}
		if found != len(p.Files) {
			return fmt.Errorf("%s: found %d of %d files", p.Name, found, len(p.Files))
		}
	}
	return nil
}
