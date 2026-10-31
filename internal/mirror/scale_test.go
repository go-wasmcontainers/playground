package mirror

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-pkgx/bottle"

	"github.com/go-wasmcontainers/playground/internal/session"
)

// TestPageCostGrowsWithTheCatalogueOnly: the page reads one catalog.json (one
// fetch, capped at 1 MiB and 1000 entries by session.FetchCatalog) and pulls an
// image's blobs only when a command uses it. This pins the part that is
// countable without a clock: catalog.json's size per image, and that the
// maximum image count fits the page's read cap.
func TestPageCostGrowsWithTheCatalogueOnly(t *testing.T) {
	var projects []bottle.CatalogProject
	for i := 0; i < 400; i++ {
		projects = append(projects, bottle.CatalogProject{
			Project: fmt.Sprintf("example%03d.org/tool", i), Versions: []string{"12.34.56"},
			Provides: []string{fmt.Sprintf("tool%03da", i), fmt.Sprintf("tool%03db", i)},
			Summary:  "a one line summary of what the tool does, kept short",
		})
	}
	oldC, oldF := catalogSource, fetchBottle
	defer func() { catalogSource, fetchBottle = oldC, oldF }()
	catalogSource = func(string, string) (bottle.Catalog, error) { return bottle.Catalog{Projects: projects}, nil }
	layer, err := TarGz(map[string][]byte{"x/bin/a.wasm": []byte("\x00asm")}, Image{}.Created)
	if err != nil {
		t.Fatal(err)
	}
	// Every provides name has a module in the fake bottle.
	fetchBottle = func(_ context.Context, ref string, _ Limits) (*BottleLayer, error) {
		w := map[string]string{}
		for i := 0; i < 400; i++ {
			for _, s := range []string{"a", "b"} {
				n := fmt.Sprintf("tool%03d%s", i, s)
				w[n] = "/x/bin/" + n + ".wasm"
			}
		}
		return &BottleLayer{Layer: layer, Wasm: w}, nil
	}
	d, err := Bottles(context.Background(), DefaultBottleLimits, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Images) != 800 {
		t.Fatalf("%d images", len(d.Images))
	}
	dir := t.TempDir()
	if err := Write(dir, d.Images); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	per := len(b) / len(d.Images)
	t.Logf("catalog.json: %d images, %d bytes, %d bytes per image", len(d.Images), len(b), per)
	if len(b) >= 1<<20 {
		t.Errorf("catalog.json (%d bytes) exceeds the page's 1 MiB read cap", len(b))
	}
	if got := session.ParseCatalog(b); len(got) != 800 {
		t.Errorf("the page reads %d of 800 entries", len(got))
	}
}
