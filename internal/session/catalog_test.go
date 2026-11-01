package session

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseCatalog(t *testing.T) {
	got := ParseCatalog([]byte(`[{"name":"hello","tags":["latest"],"description":"says hello"},{"name":"lab"}]`))
	if len(got) != 2 || got[0].Name != "hello" || got[0].Tags[0] != "latest" || got[0].Description != "says hello" || got[1].Name != "lab" {
		t.Fatalf("got %+v", got)
	}
	c := ParseCatalog([]byte(`[{"name":"a","description":"x","checked":true},{"name":"b","description":"y","checked":false},{"name":"c","description":"z"}]`))
	if c[0].Description != "x [checked]" || c[1].Description != "y [mirrored]" || c[2].Description != "z" {
		t.Errorf("checked marks: %+v", c)
	}
	for _, bad := range []string{"", "{", `{"name":"x"}`, "null", `[1]`} {
		if got := ParseCatalog([]byte(bad)); len(got) != 0 {
			t.Errorf("%q: got %+v", bad, got)
		}
	}
	// At most 1000 entries.
	var b strings.Builder
	b.WriteString("[")
	for i := range 1500 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"name":"i%d"}`, i)
	}
	b.WriteString("]")
	if got := ParseCatalog([]byte(b.String())); len(got) != 1000 || got[999].Name != "i999" {
		t.Fatalf("%d entries", len(got))
	}
}

func TestFetchCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog.json":
			w.Write([]byte(`[{"name":"hello"}]`))
		case "/huge.json": // past the 1 MiB bound: cut, so not JSON
			w.Write([]byte(`[{"name":"` + strings.Repeat("x", 2<<20) + `"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	if got := FetchCatalog(srv.URL + "/catalog.json"); len(got) != 1 || got[0].Name != "hello" {
		t.Fatalf("got %+v", got)
	}
	for _, p := range []string{"/missing.json", "/huge.json"} {
		if got := FetchCatalog(srv.URL + p); got != nil {
			t.Errorf("%s: got %+v", p, got)
		}
	}
	if got := FetchCatalog("http://127.0.0.1:1/catalog.json"); got != nil {
		t.Errorf("unreachable: got %+v", got)
	}
}

func FuzzParseCatalog(f *testing.F) {
	f.Add([]byte(`[{"name":"hello","tags":["latest"],"description":"d"}]`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if got := ParseCatalog(b); len(got) > 1000 {
			t.Fatalf("%d entries", len(got))
		}
	})
}
