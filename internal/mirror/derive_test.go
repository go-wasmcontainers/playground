package mirror

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-pkgx/bottle"
)

func TestNewest(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
		err  bool
	}{
		{[]string{"1.3.2"}, "1.3.2", false},
		{[]string{"1.9.0", "1.10.0", "1.2.0"}, "1.10.0", false}, // numeric, not lexical; order not trusted
		{[]string{"2.0.7", "v2.0.8"}, "2.0.7", false},           // catalogue versions are bare: a v-prefixed string is skipped
		{[]string{"1.8.2", "1.8.2_1"}, "1.8.2_1", false},        // a rebuild is newer
		{[]string{"1.2.3+glibc2.28", "1.2.2"}, "1.2.2", false},  // '+' is not an OCI tag character: skipped
		{[]string{"latest", "../x", "", "3.0"}, "3.0", false},   // unusable strings are skipped
		{[]string{"1.0", "1.0.0"}, "1.0.0", false},              // equal numerically: raw string breaks the tie
		{[]string{"latest", "x y"}, "", true},
		{nil, "", true},
	} {
		got, err := Newest(c.in)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("Newest(%q) = %q, %v; want %q (err %v)", c.in, got, err, c.want, c.err)
		}
	}
}

func cat(ps ...bottle.CatalogProject) bottle.Catalog {
	return bottle.Catalog{Generated: "t", Projects: ps}
}

func TestDerive(t *testing.T) {
	c := cat(
		bottle.CatalogProject{Project: "b.org/b", Versions: []string{"1.0"}, Deps: []string{"gnu.org/readline"}},
		bottle.CatalogProject{Project: "a.org", Versions: []string{"2.0", "3.0"}, Platforms: nil}, // Platforms legally empty
		bottle.CatalogProject{Project: "unpublished.org"},                                         // no Versions: not published here
		bottle.CatalogProject{Project: bottle.CatalogProjectName, Versions: []string{"1"}},        // the catalogue itself
		bottle.CatalogProject{Project: "../evil", Versions: []string{"1.0"}},
		bottle.CatalogProject{Project: "bad.org", Versions: []string{"latest"}},
		bottle.CatalogProject{Project: "a.org", Versions: []string{"9.9"}}, // duplicate
	)
	got, fs, err := Derive(c, DefaultBottleLimits)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range got {
		names = append(names, g.Project+"@"+g.Version)
	}
	if want := []string{"a.org@3.0", "b.org/b@1.0"}; !reflect.DeepEqual(names, want) {
		t.Errorf("derived %v, want %v", names, want)
	}
	kinds := map[string]bool{}
	for _, f := range fs {
		kinds[f.Kind] = true
	}
	for _, k := range []string{"bad-project-name", "bad-version", "duplicate-project"} {
		if !kinds[k] {
			t.Errorf("no %s finding in %v", k, fs)
		}
	}
	if got[0].Ref() != "ghcr.io/go-pkgx/packages/a.org:3.0" {
		t.Errorf("ref %s", got[0].Ref())
	}
}

func TestDeriveEmptyAndLimit(t *testing.T) {
	if _, _, err := Derive(cat(bottle.CatalogProject{Project: "x.org"}), DefaultBottleLimits); !errors.Is(err, ErrEmptyCatalogue) {
		t.Errorf("unpublished only: %v", err)
	}
	if _, _, err := Derive(bottle.Catalog{}, DefaultBottleLimits); !errors.Is(err, ErrEmptyCatalogue) {
		t.Errorf("empty: %v", err)
	}
	lim := DefaultBottleLimits
	lim.MaxProjects = 1
	_, _, err := Derive(cat(bottle.CatalogProject{Project: "a.org", Versions: []string{"1"}}, bottle.CatalogProject{Project: "b.org", Versions: []string{"1"}}), lim)
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("over the project limit must be an error, got %v", err)
	}
}

func TestFetchCatalogFailuresAreHard(t *testing.T) {
	old := catalogSource
	defer func() { catalogSource = old }()
	lim := DefaultBottleLimits
	catalogSource = func(string, string) (bottle.Catalog, error) {
		return bottle.Catalog{}, errors.New("signature does not verify")
	}
	if _, err := FetchCatalog(context.Background(), lim); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Errorf("signature failure: %v", err)
	}
	block := make(chan struct{})
	defer close(block)
	catalogSource = func(string, string) (bottle.Catalog, error) { <-block; return bottle.Catalog{}, nil }
	lim.Catalog = 20 * time.Millisecond
	if _, err := FetchCatalog(context.Background(), lim); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout: %v", err)
	}
}

func TestExpose(t *testing.T) {
	wasm := map[string]string{"lua": "/lua.org/v5/bin/lua.wasm", "luac": "/lua.org/v5/bin/luac.wasm", "helper": "/lua.org/v5/bin/helper.wasm"}
	got, fs := Expose(Candidate{Project: "lua.org", Version: "5", Provides: []string{"luac", "lua", "lua", "lua-gone", "BAD"}}, wasm, DefaultBottleLimits)
	var names []string
	for _, e := range got {
		names = append(names, e.Name)
	}
	// provides is a hint: every module is an image, listed names first.
	if want := []string{"lua", "luac", "helper"}; !reflect.DeepEqual(names, want) {
		t.Errorf("images %v, want %v", names, want)
	}
	kinds := map[string]int{}
	for _, f := range fs {
		kinds[f.Kind]++
	}
	if kinds["provides-without-module"] != 2 || kinds["module-not-provided"] != 1 {
		t.Errorf("findings %v", fs)
	}
	// Under the cap, unlisted modules are dropped before listed ones.
	capped := DefaultBottleLimits
	capped.MaxBinaries = 2
	got, _ = Expose(Candidate{Project: "lua.org", Version: "5", Provides: []string{"luac", "lua"}}, wasm, capped)
	if len(got) != 2 || got[0].Name != "lua" || got[1].Name != "luac" {
		t.Errorf("cap: %v", got)
	}
	// provides empty: the modules are the images (zlib's minigzip).
	got, fs = Expose(Candidate{Project: "zlib.net", Version: "1"}, map[string]string{"minigzip": "/bin/minigzip.wasm"}, DefaultBottleLimits)
	if len(got) != 1 || got[0].Name != "minigzip" || len(fs) != 1 {
		t.Errorf("fallback: %v %v", got, fs)
	}
	// per-bottle limit
	lim := DefaultBottleLimits
	lim.MaxBinaries = 1
	got, fs = Expose(Candidate{Project: "p", Version: "1", Provides: []string{"a", "b"}}, map[string]string{"a": "/bin/a.wasm", "b": "/bin/b.wasm"}, lim)
	if len(got) != 1 || len(fs) != 1 || fs[0].Kind != "too-many-binaries" {
		t.Errorf("limit: %v %v", got, fs)
	}
}

func TestName(t *testing.T) {
	all := []Exposed{
		{Name: "lua", Project: "lua.org", Binary: "lua"},
		{Name: "lua", Project: "github.com/someone/lua", Binary: "lua"}, // last segment == name: keeps it
		{Name: "tool", Project: "z.org", Binary: "tool"},
		{Name: "tool", Project: "a.org", Binary: "tool"},   // smallest project keeps it
		{Name: "hello", Project: "h.org", Binary: "hello"}, // reserved: first-party image
	}
	got, fs := Name(all, map[string]bool{"hello": true})
	by := map[string]string{}
	for _, e := range got {
		by[e.Name] = e.Project
	}
	want := map[string]string{
		"lua": "github.com/someone/lua", "lua.org-lua": "lua.org",
		"tool": "a.org", "z.org-tool": "z.org",
		"h.org-hello": "h.org",
	}
	if !reflect.DeepEqual(by, want) {
		t.Errorf("names %v, want %v", by, want)
	}
	if len(fs) != 3 {
		t.Errorf("findings %v", fs)
	}
}
