package session

import (
	"bytes"
	"context"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-wasmcontainers/engine/api"
	"github.com/go-wasmcontainers/engine/api/inproc"
	"github.com/go-wasmcontainers/engine/api/server"
	"github.com/go-wasmcontainers/engine/container"
	"github.com/go-wasmcontainers/engine/daemon"
	"github.com/go-wasmcontainers/engine/image"
	"github.com/go-wasmcontainers/engine/runtime/wazero"
	"github.com/go-wasmcontainers/playground/internal/mirror"
)

var (
	buildOnce sync.Once
	mirrorDir string
	buildErr  error
)

func buildWasm(t *testing.T, dir, name string) []byte {
	out := filepath.Join(dir, name+".wasm")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", out, "../../images/"+name)
	cmd.Env = append(cmd.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, msg)
	}
	b, err := readFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// engine serves a mirror built from the playground's own images over a
// test server, the way Pages serves it, and returns a session on it.
func engine(t *testing.T, copt container.Options) *Session {
	t.Helper()
	dir := t.TempDir()
	imgs := []mirror.Image{
		{Name: "hello", Tags: []string{"latest"}, Description: "greets", Files: map[string][]byte{"bin/hello.wasm": buildWasm(t, dir, "hello")}, Entrypoint: []string{"/bin/hello.wasm"}},
		{Name: "lab", Tags: []string{"latest"}, Files: map[string][]byte{"bin/lab.wasm": buildWasm(t, dir, "lab"), "etc/motd": []byte("welcome\n")}, Entrypoint: []string{"/bin/lab.wasm"}, Index: true},
		{Name: "greet", Tags: []string{"latest"}, Module: buildWasm(t, dir, "greet")},
	}
	site := filepath.Join(dir, "site")
	if err := mirror.Write(site, imgs); err != nil {
		t.Fatal(err)
	}
	srv := newStatic(t, site)
	e := wazero.New(wazero.Options{EngineWASI: true})
	t.Cleanup(func() { e.Close(context.Background()) })
	copt.Executor = e
	d, err := daemon.New(daemon.Options{Backend: image.NewMemoryBackend(0), Containers: copt,
		Registry: image.RegistryOptions{Mirrors: map[string]string{"docker.io": srv + "/playground"}}})
	if err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{Transport: &inproc.Transport{Handler: server.New(d, server.Info{Version: "t"})}}
	return &Session{Client: func(string) (*api.Client, error) { return api.NewClientWith(hc, "http://engine"), nil },
		Images: []Image{{Name: "hello", Tags: []string{"latest"}, Description: "greets"}}}
}

func line(t *testing.T, s *Session, l string) (string, Result) {
	t.Helper()
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r := s.Run(ctx, l, &out)
	return out.String(), r
}

// TestSpinIsKilledByTheClock: a container that never ends is stopped by the
// wall-clock limit and reports 137. Only this test needs a short limit.
func TestSpinIsKilledByTheClock(t *testing.T) {
	s := engine(t, container.Options{Timeout: 3 * time.Second})
	if out, r := line(t, s, "wctr run --rm lab spin"); r.Code != 137 {
		t.Fatalf("spin: exit %d, want 137\n%s", r.Code, out)
	}
}

// TestTranscript is the playground's target session, natively.
//
// Its wall-clock limit is generous on purpose: it used to be the 3 seconds
// that `spin` needs, which also applied to `-m 32m lab hog`, so on a slow or
// shared runner under -race hog was killed by the clock (137) before the
// memory limit stopped it (2, "out of memory") and main went red. What each
// command proves must not depend on how fast the host is.
func TestTranscript(t *testing.T) {
	s := engine(t, container.Options{Timeout: time.Minute})
	for _, c := range []struct {
		in   string
		code int
		want []string
	}{
		{"", 0, nil},
		{"help", 0, []string{"wctr pull IMAGE", "hello:latest", "greets"}},
		{"wctr pull hello", 0, []string{"Downloaded newer image for hello:latest"}},
		{"wctr run --rm hello", 0, []string{"Hello from a WebAssembly container!"}},
		{"wctr run --rm greet", 0, []string{"Unable to find image", "Hello, world!"}},
		{"wctr run --rm -e NAME=lab greet", 0, []string{"Hello, lab!"}},
		{"wctr run -d --name x lab count 3 10", 0, nil},
		{"wctr run --name y lab exit 7", 7, []string{"bye"}},
		{"wctr run --rm lab cat /etc/motd", 0, []string{"welcome"}},
		{"wctr run --rm lab echo 'two  words'", 0, []string{"two  words"}},
		{"wctr run --rm -m 32m lab hog", 2, []string{"out of memory"}},
		{"wctr run --rm lab bench 200", 0, []string{"200 stat calls"}},
		{"wctr ps -a", 0, []string{"Exited (7)", " y\n"}},
		{"wctr logs y", 0, []string{"bye"}},
		{"wctr images", 0, []string{"hello", "lab", "greet"}},
		{"wctr rm x y", 0, []string{"x", "y"}},
		{"wctr ps -a -q", 0, nil},
		{"wctr run --rm nosuch", 125, []string{"Unable to find image"}},
		{"docker ps", 127, []string{"wctr"}},
		{"ls", 127, []string{"command not found"}},
		{"wctr run 'open", 2, []string{"unterminated"}},
		{"clear", 0, nil},
	} {
		out, r := line(t, s, c.in)
		if r.Code != c.code {
			t.Fatalf("%q: exit %d, want %d\n%s", c.in, r.Code, c.code, out)
		}
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Fatalf("%q: output lacks %q:\n%s", c.in, w, out)
			}
		}
		if c.in == "clear" && !r.Clear {
			t.Fatal("clear not signalled")
		}
		t.Logf("$ %s\n%s", c.in, out)
	}
}

func TestEscapesAndRate(t *testing.T) {
	var out bytes.Buffer
	now := time.Unix(0, 0)
	s := &Session{Rate: 10, Burst: 20, Now: func() time.Time { return now }}
	w := s.writer(&out)
	w.Write([]byte("\x1b]52;c;ZXZpbA==\x07ok\x1b[31m!\n"))
	if out.String() != "ok\x1b[31m!\n" {
		t.Fatalf("%q", out.String())
	}
	out.Reset()
	w.Write([]byte(strings.Repeat("x", 30)))
	w.Write([]byte("more"))
	if !strings.Contains(out.String(), "rate limited") || strings.Count(out.String(), "rate limited") != 1 {
		t.Fatalf("%q", out.String())
	}
	now = now.Add(2 * time.Second)
	out.Reset()
	w.Write([]byte("later"))
	if out.String() != "later" {
		t.Fatalf("refill: %q", out.String())
	}
}
