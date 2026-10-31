package mirror

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-pkgx/bottle"
)

// fakeWorld serves a catalogue and bottles without a network. Adding a
// project to it is the only change needed to get its images: the derived list
// grows with no code change.
func fakeWorld(t *testing.T, projects ...bottle.CatalogProject) {
	t.Helper()
	oldC, oldF := catalogSource, fetchBottle
	t.Cleanup(func() { catalogSource, fetchBottle = oldC, oldF })
	catalogSource = func(osn, arch string) (bottle.Catalog, error) {
		if osn != "wasip1" || arch != "wasm" {
			return bottle.Catalog{}, fmt.Errorf("asked for %s/%s", osn, arch)
		}
		return bottle.Catalog{Generated: "now", Projects: projects}, nil
	}
	fetchBottle = func(_ context.Context, ref string, lim Limits) (*BottleLayer, error) {
		proj := strings.TrimPrefix(ref, Registry+"/")
		proj = proj[:strings.LastIndexByte(proj, ':')]
		for _, p := range projects {
			if p.Project != proj {
				continue
			}
			switch p.Summary {
			case "big":
				return nil, &LimitError{"too big"}
			case "net":
				return nil, errors.New("connection reset")
			}
			w := map[string]string{}
			for _, b := range p.Provides {
				w[b] = "/" + proj + "/bin/" + b + ".wasm"
			}
			return &BottleLayer{Layer: []byte("layer-" + proj), Wasm: w}, nil
		}
		return nil, errors.New("no such bottle " + ref)
	}
}

func TestBottlesGrowWithTheCatalogue(t *testing.T) {
	base := []bottle.CatalogProject{
		{Project: "a.org", Versions: []string{"1.0"}, Provides: []string{"a"}},
		{Project: "b.org", Versions: []string{"2.1", "2.0"}, Provides: []string{"b", "b2"}},
		{Project: "skip.org"},
	}
	fakeWorld(t, base...)
	d, err := Bottles(context.Background(), DefaultBottleLimits, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Images) != 3 {
		t.Fatalf("%d images", len(d.Images))
	}
	b2 := d.Images[2]
	if b2.Name != "b2" || b2.Version != "2.1" || b2.Tags[0] != "2.1" || b2.Tags[1] != "latest" || b2.Entrypoint[0] != "b2" || b2.ModulePath != "/b.org/bin/b2.wasm" || !strings.HasPrefix(b2.Env[0], "PATH=/b.org/bin:") {
		t.Errorf("b2: %+v", b2)
	}

	// Two more projects published: five images, same code.
	fakeWorld(t, append(base,
		bottle.CatalogProject{Project: "c.org", Versions: []string{"0.1"}, Provides: []string{"c"}},
		bottle.CatalogProject{Project: "d.org", Versions: []string{"3"}, Provides: []string{"d"}})...)
	d, err = Bottles(context.Background(), DefaultBottleLimits, nil, nil)
	if err != nil || len(d.Images) != 5 {
		t.Fatalf("grown: %d images, %v", len(d.Images), err)
	}
}

func TestBottlesFailures(t *testing.T) {
	ctx := context.Background()
	// Empty catalogue: hard error, no fallback.
	fakeWorld(t)
	if _, err := Bottles(ctx, DefaultBottleLimits, nil, nil); !errors.Is(err, ErrEmptyCatalogue) {
		t.Errorf("empty: %v", err)
	}
	// A bottle over its limit is a finding that costs only itself.
	fakeWorld(t,
		bottle.CatalogProject{Project: "ok.org", Versions: []string{"1"}, Provides: []string{"ok"}},
		bottle.CatalogProject{Project: "huge.org", Versions: []string{"1"}, Provides: []string{"huge"}, Summary: "big"})
	d, err := Bottles(ctx, DefaultBottleLimits, nil, nil)
	if err != nil || len(d.Images) != 1 || len(d.Findings) != 1 || d.Findings[0].Kind != "over-limit" {
		t.Errorf("limit: %+v %v", d, err)
	}
	// A network error is not a finding: the catalogue said it exists.
	fakeWorld(t,
		bottle.CatalogProject{Project: "ok.org", Versions: []string{"1"}, Provides: []string{"ok"}},
		bottle.CatalogProject{Project: "down.org", Versions: []string{"1"}, Provides: []string{"x"}, Summary: "net"})
	if _, err := Bottles(ctx, DefaultBottleLimits, nil, nil); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("network: %v", err)
	}
	// Image count limit.
	fakeWorld(t,
		bottle.CatalogProject{Project: "a.org", Versions: []string{"1"}, Provides: []string{"a", "b"}})
	lim := DefaultBottleLimits
	lim.MaxImages = 1
	if _, err := Bottles(ctx, lim, nil, nil); err == nil {
		t.Errorf("image limit not enforced")
	}
	// Site size budget: a bottle that gives two images is counted twice.
	fakeWorld(t, bottle.CatalogProject{Project: "a.org", Versions: []string{"1"}, Provides: []string{"a", "b"}})
	d, err = Bottles(ctx, DefaultBottleLimits, nil, nil)
	if err != nil || d.LayerBytes != 2*int64(len("layer-a.org")) {
		t.Errorf("layer bytes: %+v %v", d, err)
	}
	lim = DefaultBottleLimits
	lim.MaxSiteBytes = 10
	if _, err := Bottles(ctx, lim, nil, nil); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Errorf("size budget not enforced: %v", err)
	}
	// Catalogue failure.
	fakeWorld(t)
	catalogSource = func(string, string) (bottle.Catalog, error) { return bottle.Catalog{}, errors.New("bad signature") }
	if _, err := Bottles(ctx, DefaultBottleLimits, nil, nil); err == nil {
		t.Errorf("signature failure swallowed")
	}
}
