package session

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func readFile(p string) ([]byte, error) { return os.ReadFile(p) }

// newStatic serves dir under /playground/ like GitHub Pages: plain files,
// Content-Type from the extension (octet-stream for manifests, which have
// none), no registry headers.
func newStatic(t *testing.T, dir string) string {
	fs := http.FileServer(http.Dir(dir))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := strings.CutPrefix(r.URL.Path, "/playground")
		if !ok {
			http.NotFound(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = p
		w.Header().Set("Content-Type", "application/octet-stream")
		fs.ServeHTTP(w, r2)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
