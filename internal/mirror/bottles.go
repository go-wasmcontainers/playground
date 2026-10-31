package mirror

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-pkgx/bottle"
)

// fetchBottle is the seam tests replace.
var fetchBottle = FetchBottle

// Derived is everything Bottles found.
type Derived struct {
	Images   []Image
	Findings []Finding
	Catalog  bottle.Catalog
}

// Bottles builds the mirror's pkgx images from the signed wasip1/wasm
// catalogue: no list is typed anywhere, so a bottle published tomorrow is
// mirrored by the next run with no code change.
//
// Failures: an unverifiable or empty catalogue, a network error, a timeout and
// an over-limit count of projects or images are errors. A bottle that is over
// a size limit, whose tar holds an unsafe path, or that has no module for what
// provides says, is a Finding and costs only itself. reserved names are images
// the mirror already has.
func Bottles(ctx context.Context, lim Limits, reserved map[string]bool, log io.Writer) (*Derived, error) {
	ctx, cancel := context.WithTimeout(ctx, lim.Total)
	defer cancel()
	logf := func(format string, a ...any) {
		if log != nil {
			fmt.Fprintf(log, format+"\n", a...)
		}
	}
	cat, err := FetchCatalog(ctx, lim)
	if err != nil {
		return nil, err
	}
	cands, findings, err := Derive(cat, lim)
	if err != nil {
		return nil, err
	}
	logf("catalogue %s: %d project(s) in all, %d published for %s/%s", cat.Generated, len(cat.Projects), len(cands), StageOS, StageArch)

	type result struct {
		layer *BottleLayer
		err   error
	}
	res := make([]result, len(cands))
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(1, lim.Parallel))
	for i, c := range cands {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			l, err := fetchBottle(ctx, c.Ref(), lim)
			res[i] = result{l, err}
			if err == nil {
				logf("fetched %s:%s, %d bytes, %s", c.Project, c.Version, len(l.Layer), time.Since(start).Round(time.Millisecond))
			}
		}()
	}
	wg.Wait()

	var exposed []Exposed
	layers := map[string]*BottleLayer{} // project -> layer
	for i, c := range cands {
		r := res[i]
		if r.err != nil {
			var le *LimitError
			var ue *UnsafeError
			switch {
			case errors.As(r.err, &le):
				findings = append(findings, Finding{Project: c.Project, Kind: "over-limit", Detail: r.err.Error()})
				continue
			case errors.As(r.err, &ue):
				findings = append(findings, Finding{Project: c.Project, Kind: "unsafe-archive", Detail: r.err.Error()})
				continue
			}
			return nil, fmt.Errorf("%s: %w", c.Ref(), r.err)
		}
		e, fs := Expose(c, r.layer.Wasm, lim)
		findings = append(findings, fs...)
		if len(e) == 0 {
			findings = append(findings, Finding{Project: c.Project, Kind: "no-image", Detail: "the bottle yields no image"})
			continue
		}
		layers[c.Project] = r.layer
		exposed = append(exposed, e...)
	}
	named, fs := Name(exposed, reserved)
	findings = append(findings, fs...)
	if len(named) == 0 {
		return nil, fmt.Errorf("%d project(s) in the catalogue and not one image could be built from them: %w", len(cands), ErrEmptyCatalogue)
	}
	if len(named) > lim.MaxImages {
		return nil, fmt.Errorf("%d images, the limit is %d (catalog.json is read up to 1000 entries): raise Limits.MaxImages on purpose, the mirror does not truncate", len(named), lim.MaxImages)
	}
	d := &Derived{Catalog: cat}
	for _, e := range named {
		bl := layers[e.Project]
		bin := path.Dir(bl.Wasm[e.Binary])
		desc := "pkgx bottle " + e.Project + ":" + e.Version + " (" + strings.TrimPrefix(bl.Wasm[e.Binary], "/") + ")"
		if e.Summary != "" {
			desc += " " + e.Summary
		}
		d.Images = append(d.Images, Image{
			Name: e.Name, Tags: []string{e.Version, "latest"},
			Description: desc,
			Layer:       bl.Layer,
			LayerZstd:   bl.Zstd,
			Entrypoint:  []string{bl.Wasm[e.Binary]},
			Env:         []string{"PATH=" + bin + ":/usr/local/bin:/usr/bin:/bin"},
			Created:     time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
			Project:     e.Project, Version: e.Version,
		})
		logf("image %s: pkgx bottle %s:%s, %d bytes", e.Name, e.Project, e.Version, len(bl.Layer))
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Project != findings[j].Project {
			return findings[i].Project < findings[j].Project
		}
		return findings[i].Kind < findings[j].Kind
	})
	d.Findings = findings
	return d, nil
}

// WriteFindings writes findings.json next to catalog.json.
func WriteFindings(dir string, fs []Finding) error {
	if fs == nil {
		fs = []Finding{}
	}
	return os.WriteFile(filepath.Join(dir, "findings.json"), mustJSON(fs), 0o644)
}
