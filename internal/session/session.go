// Package session turns terminal lines into wctr commands run against an
// engine, and makes what they print safe for the terminal: escape
// sequences filtered (termfilter), output rate bounded.
//
// It is plain Go, so the whole path from a typed line to the text the
// terminal receives is tested natively; the browser build only wires it
// to postMessage.
package session

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-wasmcontainers/engine/api"
	"github.com/go-wasmcontainers/engine/cli"

	"github.com/go-wasmcontainers/playground/internal/shell"
	"github.com/go-wasmcontainers/playground/internal/termfilter"
)

// Image is a catalog entry for help.
type Image struct {
	Name        string
	Tags        []string
	Description string
}

// Session runs lines.
type Session struct {
	// Client reaches the engine.
	Client func(host string) (*api.Client, error)
	// Version is the engine version wctr reports.
	Version string
	// Images are listed by help.
	Images []Image
	// Rate bounds output bytes per second, Burst the bytes allowed at once
	// (defaults 256 KiB/s and 1 MiB).
	Rate, Burst int
	// Now is the clock of the rate limiter (default time.Now).
	Now func() time.Time
}

// Result is how a line ended.
type Result struct {
	Code int
	// Clear asks the terminal to clear its screen.
	Clear bool
}

// Run runs one line, writing what it prints to out.
func (s *Session) Run(ctx context.Context, line string, out io.Writer) Result {
	w := s.writer(out)
	defer w.flush()
	words, err := shell.Split(line)
	if err != nil {
		fmt.Fprintln(w, "playground:", err)
		return Result{Code: 2}
	}
	if len(words) == 0 {
		return Result{}
	}
	switch words[0] {
	case "help":
		s.help(w)
		return Result{}
	case "clear":
		return Result{Clear: true}
	case "wctr":
		cmd := cli.RootCmd(cli.Options{
			Version:     s.Version,
			DefaultHost: "inproc://engine",
			Client:      s.Client,
			LookupEnv:   func(string) (string, bool) { return "", false },
		})
		cmd.SetOut(w)
		cmd.SetErr(w)
		cmd.SetArgs(words[1:])
		if err := cmd.ExecuteContext(ctx); err != nil {
			return Result{Code: cli.Report(w, err)}
		}
		return Result{}
	case "docker", "podman", "nerdctl":
		fmt.Fprintf(w, "%s: command not found; this playground's container command is wctr (try: wctr run --rm hello)\n", words[0])
		return Result{Code: 127}
	}
	fmt.Fprintf(w, "%s: command not found (type help)\n", words[0])
	return Result{Code: 127}
}

func (s *Session) help(w io.Writer) {
	fmt.Fprint(w, `This terminal runs wctr, a container CLI, against an engine running in this page.
Containers are WebAssembly (wasip1) modules; nothing leaves your browser.

  wctr pull IMAGE                 fetch an image
  wctr images                     list images
  wctr run [--rm] [-d] [--name N] [-e K=V] [-w DIR] [-m 64m] IMAGE [ARGS...]
  wctr ps [-a]                    list containers
  wctr logs [-f] CONTAINER        show a container's output
  wctr rm [-f] CONTAINER...       remove containers
  wctr rmi IMAGE...               remove images
  wctr inspect NAME               details, as JSON
  wctr kill CONTAINER             stop a running container
  clear                           clear the screen
Ctrl-C interrupts the running command. Up/Down recall previous lines.
`)
	if len(s.Images) > 0 {
		fmt.Fprintln(w, "\nImages:")
		imgs := append([]Image(nil), s.Images...)
		sort.Slice(imgs, func(i, j int) bool { return imgs[i].Name < imgs[j].Name })
		for _, i := range imgs {
			fmt.Fprintf(w, "  %-22s %s\n", i.Name+":"+strings.Join(i.Tags, ","), i.Description)
		}
	}
	fmt.Fprintln(w, "\nTry: wctr run --rm hello")
}

// limiter filters escapes and bounds the rate of what reaches out.
type limiter struct {
	mu      sync.Mutex
	out     io.Writer
	f       termfilter.Filter
	rate    float64
	burst   float64
	tokens  float64
	last    time.Time
	now     func() time.Time
	dropped bool
}

func (s *Session) writer(out io.Writer) *limiter {
	rate, burst := s.Rate, s.Burst
	if rate <= 0 {
		rate = 256 << 10
	}
	if burst <= 0 {
		burst = 1 << 20
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	return &limiter{out: out, rate: float64(rate), burst: float64(burst), tokens: float64(burst), last: now(), now: now}
}

func (l *limiter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	l.tokens += t.Sub(l.last).Seconds() * l.rate
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.last = t
	safe := l.f.Filter(p)
	if float64(len(safe)) > l.tokens {
		if !l.dropped {
			l.dropped = true
			io.WriteString(l.out, "\r\n[output rate limited: some output was not shown; see wctr logs]\r\n")
		}
		return len(p), nil
	}
	l.tokens -= float64(len(safe))
	if len(safe) > 0 {
		if _, err := l.out.Write(safe); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (l *limiter) flush() {}
