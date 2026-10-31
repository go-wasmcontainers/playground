package mirror

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/go-pkgx/bottle"
)

// The platform the playground serves. Named once: the catalogue fetch, the
// bottle selection and the image config must agree (go-pkgx.github.io
// tools/wasmstage/main.go:81-84 does the same).
const (
	StageOS   = "wasip1"
	StageArch = "wasm"
)

// Registry is where the bottles live (bottle.DistBase is "oci://ghcr.io/go-pkgx/packages";
// pkgx.go:34 of github.com/go-pkgx/bottle v0.60.0).
const Registry = "ghcr.io/go-pkgx/packages"

// Limits bound everything the catalogue and the bottles can make the mirror
// do. Both are untrusted input: the catalogue is signed, a layer is only
// digest-checked against a manifest the registry served.
type Limits struct {
	MaxProjects int   // projects the catalogue may name for this platform
	MaxLayer    int64 // compressed bytes of one bottle
	MaxUnpacked int64 // decompressed bytes of one bottle (bomb limit)
	MaxEntries  int   // tar entries of one bottle
	MaxBinaries int   // images exposed per bottle
	MaxImages   int   // images in the whole mirror (catalog.json is read up to 1000)
	// MaxSiteBytes bounds the layers the mirror publishes, counted once per
	// image (each image has its own /v2 repository, so a bottle that gives two
	// images is stored twice). GitHub Pages refuses a site over 1 GB; the
	// engine, the page and the first-party images take some of it.
	MaxSiteBytes int64
	Parallel     int           // concurrent bottle fetches
	Catalog      time.Duration // timeout of the catalogue fetch
	Total        time.Duration // timeout of the whole derivation
}

// DefaultBottleLimits are the limits the site build uses.
var DefaultBottleLimits = Limits{
	MaxProjects: 400,
	MaxLayer:    100 << 20,
	MaxUnpacked: 512 << 20,
	MaxEntries:  20000,
	MaxBinaries: 8,
	MaxImages:   900,
	// 700 MiB: the Pages ceiling is 1 GB (1000 MB); the rest is the engine,
	// the dashboard, the first-party and pinned images.
	MaxSiteBytes: 700 << 20,
	Parallel:     6,
	Catalog:      3 * time.Minute,
	Total:        30 * time.Minute,
}

// Candidate is one project the catalogue says is published for wasip1/wasm,
// at the version chosen by Newest.
type Candidate struct {
	Project  string
	Version  string
	Provides []string
	Summary  string
}

// Ref is the registry reference of the candidate's bottle.
// The repository path is lowercased, as the registry requires and as
// bottle's OCIClient.repoName does (oci.go:198-201): BurntSushi/toml is
// github.com/burntsushi/toml.
func (c Candidate) Ref() string {
	return Registry + "/" + strings.ToLower(c.Project) + ":" + c.Version
}

// Finding is a difference worth telling the bottle's owner about. Findings
// are listed, never dropped.
type Finding struct {
	Project string `json:"project"`
	Image   string `json:"image,omitempty"`
	Kind    string `json:"kind"`
	Detail  string `json:"detail"`
}

// ErrEmptyCatalogue is returned when the catalogue names no wasip1/wasm
// project: a catalogue that answered nothing and a platform with nothing on
// it print the same list, so this is a failure and there is no fallback.
var ErrEmptyCatalogue = errors.New("the catalogue lists no project published for wasip1/wasm")

// catalogSource is the seam tests replace. It is bottle.FetchCatalog, which
// verifies the signature (catalog.go:695-704): a signature failure is an
// error, never a fallback.
var catalogSource = func(osn, arch string) (bottle.Catalog, error) {
	c, err := bottle.NewOCIClient(bottle.DistBase)
	if err != nil {
		return bottle.Catalog{}, err
	}
	return bottle.FetchCatalog(c, osn, arch)
}

// FetchCatalog reads and verifies the wasip1/wasm catalogue, bounded by a
// timeout (bottle.FetchCatalog takes no context, so it runs in a goroutine
// and is abandoned on expiry).
func FetchCatalog(ctx context.Context, lim Limits) (bottle.Catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, lim.Catalog)
	defer cancel()
	type res struct {
		c   bottle.Catalog
		err error
	}
	ch := make(chan res, 1)
	src := catalogSource
	go func() {
		c, err := src(StageOS, StageArch)
		ch <- res{c, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return bottle.Catalog{}, fmt.Errorf("catalogue %s/%s: %w", StageOS, StageArch, r.err)
		}
		return r.c, nil
	case <-ctx.Done():
		return bottle.Catalog{}, fmt.Errorf("catalogue %s/%s: %w", StageOS, StageArch, ctx.Err())
	}
}

// Newest picks the version to mirror among those a catalogue entry lists.
//
// The rule: the highest by numeric components, then by rebuild counter
// (`1.8.2_1` is newer than `1.8.2`), then by raw string, so the pick is
// deterministic. The order of the list is not trusted (CatalogProject.Versions
// says newest-first but the published catalogue carries exactly one,
// catalog.go:66-83). A string that is not version-shaped
// (bottle.ValidateVersionString), does not start with a digit, or cannot be an
// OCI tag ('+' is a legal version character and an illegal tag character) is
// skipped; if none is left it is an error.
func Newest(versions []string) (string, error) {
	var best bottle.Ver
	found := false
	var bad []string
	for _, s := range versions {
		if err := bottle.ValidateVersionString(s); err != nil || !validTag(s) || s[0] < '0' || s[0] > '9' {
			bad = append(bad, s)
			continue
		}
		v := bottle.ParseVer(s)
		if !found || verLess(best, v) {
			best, found = v, true
		}
	}
	if !found {
		return "", fmt.Errorf("no usable version among %q", versions)
	}
	return best.Raw, nil
}

func verLess(a, b bottle.Ver) bool {
	for i := 0; i < len(a.Nums) || i < len(b.Nums); i++ {
		var x, y int
		if i < len(a.Nums) {
			x = a.Nums[i]
		}
		if i < len(b.Nums) {
			y = b.Nums[i]
		}
		if x != y {
			return x < y
		}
	}
	if a.Rebuild != b.Rebuild {
		return a.Rebuild < b.Rebuild
	}
	return a.Raw < b.Raw
}

var (
	tagRE   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	imageRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

func validTag(s string) bool { return tagRE.MatchString(s) }

// Derive turns a catalogue into the candidates to mirror.
//
// Copied from go-pkgx.github.io tools/wasmstage/main.go:116-136 (derive):
// a project counts when its Versions is non-empty (Platforms is legally
// empty, catalog.go:84-85), the catalogue's own project is skipped, and there
// is no built-in list. Dependencies are ignored on purpose: a wasip1 module is
// statically linked and the catalogue's deps are read from the host recipe
// (lua lists readline; go-pkgx/bk#376).
func Derive(cat bottle.Catalog, lim Limits) ([]Candidate, []Finding, error) {
	var out []Candidate
	var fs []Finding
	seen := map[string]bool{}
	for _, p := range cat.Projects {
		if p.Project == bottle.CatalogProjectName || len(p.Versions) == 0 {
			continue
		}
		if err := bottle.ValidateProjectName(p.Project); err != nil {
			fs = append(fs, Finding{Project: p.Project, Kind: "bad-project-name", Detail: err.Error()})
			continue
		}
		if seen[p.Project] {
			fs = append(fs, Finding{Project: p.Project, Kind: "duplicate-project", Detail: "listed twice in the catalogue; the first entry is used"})
			continue
		}
		seen[p.Project] = true
		v, err := Newest(p.Versions)
		if err != nil {
			fs = append(fs, Finding{Project: p.Project, Kind: "bad-version", Detail: err.Error()})
			continue
		}
		out = append(out, Candidate{Project: p.Project, Version: v, Provides: append([]string(nil), p.Provides...), Summary: cleanText(p.Summary, 120)})
	}
	if len(out) == 0 {
		return nil, fs, fmt.Errorf("%w (%d projects in the catalogue)", ErrEmptyCatalogue, len(cat.Projects))
	}
	if len(out) > lim.MaxProjects {
		return nil, fs, fmt.Errorf("the catalogue lists %d projects for wasip1/wasm, the limit is %d: raise Limits.MaxProjects on purpose, the mirror does not truncate", len(out), lim.MaxProjects)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out, fs, nil
}

// cleanText keeps printable text only and bounds its length: a summary is
// shown in a terminal.
func cleanText(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= max {
			break
		}
		if unicode.IsPrint(r) {
			b.WriteRune(r)
			n++
		} else if unicode.IsSpace(r) {
			b.WriteByte(' ')
			n++
		}
	}
	return strings.TrimSpace(b.String())
}

// Exposed is one image to publish: a binary of a bottle.
type Exposed struct {
	Name    string // image name, library/<Name>
	Project string
	Version string
	Binary  string // bin/<Binary>.wasm
	Summary string
}

// Expose decides which images a bottle gives, and how they are named.
//
// Rule (documented in the README): every regular file bin/<name>.wasm in the
// bottle is an image, at most Limits.MaxBinaries of them (names in `provides`
// first, then sorted). `provides` is a hint only: it is wrong in both
// directions on wasip1/wasm (go-pkgx/bk#376). A provides name without a module,
// and a module provides does not list, are findings. All of a bottle's images
// share the same layer (every binary is in each image's /bin); only the
// entrypoint differs.
//
// Names are the binary names. When several projects give the same name the
// project whose last path segment equals it keeps it (else the smallest project
// name), and the others are published as <project-slug>-<binary>. A name that
// collides with a reserved one (a first-party image) takes the slug form too.
func Expose(c Candidate, wasm map[string]string, lim Limits) ([]Exposed, []Finding) {
	var out []Exposed
	var fs []Finding
	provided := map[string]bool{}
	for _, n := range c.Provides {
		provided[n] = true
	}
	// On wasip1/wasm `provides` is copied from the host recipe and is wrong in
	// both directions (go-pkgx/bk#376): the bottle's modules are the authority.
	// Every bin/<n>.wasm is an image; the names `provides` lists come first, so
	// the per-bottle cap drops unlisted modules before listed ones.
	var mods []string
	for b := range wasm {
		mods = append(mods, b)
	}
	sort.Slice(mods, func(i, j int) bool {
		if provided[mods[i]] != provided[mods[j]] {
			return provided[mods[i]]
		}
		return mods[i] < mods[j]
	})
	for _, n := range mods {
		if !provided[n] {
			fs = append(fs, Finding{Project: c.Project, Kind: "module-not-provided", Detail: fmt.Sprintf("bin/%s.wasm is in the bottle but not in provides", n)})
		}
		if !imageRE.MatchString(n) {
			fs = append(fs, Finding{Project: c.Project, Kind: "bad-image-name", Detail: fmt.Sprintf("binary %q is not a usable image name", n)})
			continue
		}
		if len(out) >= lim.MaxBinaries {
			fs = append(fs, Finding{Project: c.Project, Kind: "too-many-binaries", Detail: fmt.Sprintf("%q not exposed: limit is %d per bottle", n, lim.MaxBinaries)})
			continue
		}
		out = append(out, Exposed{Name: n, Project: c.Project, Version: c.Version, Binary: n, Summary: c.Summary})
	}
	var missing []string
	for n := range provided {
		if _, ok := wasm[n]; !ok {
			missing = append(missing, n)
		}
	}
	sort.Strings(missing)
	for _, n := range missing {
		fs = append(fs, Finding{Project: c.Project, Kind: "provides-without-module", Detail: fmt.Sprintf("provides %q but the bottle has no bin/%s.wasm", n, n)})
	}
	return out, fs
}

// Name assigns final image names across projects (see Expose). reserved holds
// names that are taken (first-party images).
func Name(all []Exposed, reserved map[string]bool) ([]Exposed, []Finding) {
	byName := map[string][]int{}
	for i, e := range all {
		byName[e.Name] = append(byName[e.Name], i)
	}
	var fs []Finding
	out := append([]Exposed(nil), all...)
	taken := map[string]bool{}
	for r := range reserved {
		taken[r] = true
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		idx := byName[n]
		sort.Slice(idx, func(a, b int) bool { return all[idx[a]].Project < all[idx[b]].Project })
		win := -1
		if !reserved[n] {
			win = idx[0]
			for _, i := range idx {
				if lastSeg(all[i].Project) == n {
					win = i
					break
				}
			}
			taken[n] = true
		}
		for _, i := range idx {
			if i == win {
				continue
			}
			slug := strings.ToLower(strings.NewReplacer("/", "-").Replace(all[i].Project)) + "-" + all[i].Binary
			if !imageRE.MatchString(slug) || taken[slug] {
				out[i].Name = ""
				fs = append(fs, Finding{Project: all[i].Project, Image: n, Kind: "name-collision", Detail: fmt.Sprintf("%q is taken and %q is unusable: not published", n, slug)})
				continue
			}
			taken[slug] = true
			out[i].Name = slug
			fs = append(fs, Finding{Project: all[i].Project, Image: slug, Kind: "renamed", Detail: fmt.Sprintf("binary %q is taken by another image; published as %q", n, slug)})
		}
	}
	kept := out[:0]
	for _, e := range out {
		if e.Name != "" {
			kept = append(kept, e)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Name < kept[j].Name })
	return kept, fs
}

func lastSeg(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
