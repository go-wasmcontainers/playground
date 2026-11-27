package session

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/go-wasmcontainers/engine/api"

	"github.com/go-wasmcontainers/playground/internal/composefs"
)

func noEngine() *Session {
	return &Session{Client: func(string) (*api.Client, error) { return nil, errors.New("no engine in this test") }}
}

// TestComposeNeverReadsTheHost: a page has no host directory. With a
// compose.yaml in the process's working directory, `wctr compose config` must
// still say there is no file: it reads the session's own directory.
// (Before the session passed one, compose opened the working directory, which
// is the failure "getwd: not implemented on js" in the browser.)
func TestComposeNeverReadsTheHost(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/compose.yaml", []byte("services:\n  host:\n    image: hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	out, r := line(t, noEngine(), "wctr compose config")
	if r.Code == 0 || !strings.Contains(out, "no Compose file found") || strings.Contains(out, "host") {
		t.Fatalf("status %d, output:\n%s", r.Code, out)
	}
}

// TestComposeReadsWhatWasImported: typed `wctr compose` sees the imported
// files, and a refused import leaves them as they were.
func TestComposeReadsWhatWasImported(t *testing.T) {
	s := noEngine()
	if err := s.ComposeFiles().Import([]composefs.File{{Name: "compose.yaml", Data: []byte("name: demo\nservices:\n  web:\n    image: hello\n")}}); err != nil {
		t.Fatal(err)
	}
	out, r := line(t, s, "wctr compose config")
	if r.Code != 0 || !strings.Contains(out, "web:") || !strings.Contains(out, "name: demo") {
		t.Fatalf("status %d, output:\n%s", r.Code, out)
	}
	if err := s.ComposeFiles().Import([]composefs.File{{Name: "../x", Data: nil}}); err == nil {
		t.Fatal("a name with a separator was imported")
	}
	if out, r = line(t, s, "wctr compose config"); r.Code != 0 || !strings.Contains(out, "web:") {
		t.Fatalf("a refused import changed the directory: status %d\n%s", r.Code, out)
	}
}
