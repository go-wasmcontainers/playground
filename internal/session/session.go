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

	"github.com/spf13/cobra"

	buildcli "github.com/go-wasmcontainers/build/cli"
	composecli "github.com/go-wasmcontainers/compose/cli"
	"github.com/go-wasmcontainers/engine/api"
	"github.com/go-wasmcontainers/engine/cli"
	"github.com/go-wasmcontainers/engine/cli/lineedit"

	"github.com/go-wasmcontainers/playground/internal/termfilter"
)

// Builtins are the playground's own commands, for completion of the
// first word.
var Builtins = []cli.Candidate{
	{Value: "wctr", Description: "the container command"},
	{Value: "help", Description: "what this terminal can do"},
	{Value: "clear", Description: "clear the screen"},
}

// LabCommands complete `wctr run lab <TAB>`.
var LabCommands = []cobra.Completion{
	"echo\tprint the arguments", "env\tprint the environment", "pwd\tprint the working directory",
	"ls\tlist a directory", "cat\tprint a file", "write\twrite a file (in this container only)",
	"count\tcount, slowly", "sleep\tsleep MS milliseconds", "spin\tloop for ever (see the timeout)",
	"hog\teat memory (see the limit)", "bench\ttime N WASI calls", "fs\ta file-system workout",
	"exit\texit with a status", "esc\tprint escapes (see the filter)",
}

// NetCommands are the net image's commands, for completion.
var NetCommands = []cobra.Completion{
	"serve\tHTTP server (port 80)", "get\tGET a URL", "bench\tlatency and throughput of a server",
	"udp-echo\techo datagrams (port 7)", "udp\tsend a datagram, print the reply", "lookup\tresolve a name",
	"dial\topen a TCP connection",
}

// CLIOptions is the wctr command tree this session runs and completes.
func (s *Session) CLIOptions() cli.Options {
	return cli.Options{
		Version:             s.Version,
		DefaultHost:         "inproc://engine",
		Client:              s.Client,
		LookupEnv:           func(string) (string, bool) { return "", false },
		NoCompletionCommand: true,
		// wctr build and wctr compose, as cmd/wctr wires them.
		Extra: []func(cli.Engine) *cobra.Command{
			func(e cli.Engine) *cobra.Command { return buildcli.ForEngine(e) },
			func(e cli.Engine) *cobra.Command { return composecli.Command(composecli.Options{Client: e.Client}) },
		},
		PullCandidates: func(context.Context) []cobra.Completion {
			var out []cobra.Completion
			for _, i := range s.Images {
				out = append(out, cobra.CompletionWithDesc(i.Name, "pullable: "+i.Description))
			}
			return out
		},
		RunArgs: func(image string, args []string) []cobra.Completion {
			switch {
			case (image == "lab" || image == "lab:latest") && len(args) == 0:
				return LabCommands
			case (image == "net" || image == "net:latest") && len(args) == 0:
				return NetCommands
			}
			return nil
		},
	}
}

// Completer completes the playground's lines: builtins, then wctr.
func (s *Session) Completer() lineedit.Completer {
	return lineedit.WctrCompleter(s.CLIOptions(), Builtins)
}

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

// Interactive says whether a line runs a container with its input
// attached (wctr run -i, exec -i, start -ai), read with wctr's own flags: then
// what the user types, Ctrl-C and Ctrl-D included, is the container's.
func (s *Session) Interactive(line string) bool {
	words, err := lineedit.Split(line)
	if err != nil || len(words) < 2 || words[0] != "wctr" {
		return false
	}
	root := cli.RootCmd(s.CLIOptions())
	root.TraverseChildren = true
	sub, rest, err := root.Traverse(words[1:])
	if err != nil || sub == root || (sub.Name() != "run" && sub.Name() != "exec" && sub.Name() != "start") {
		return false
	}
	if err := sub.ParseFlags(rest); err != nil {
		return false
	}
	i, err := sub.Flags().GetBool("interactive")
	return err == nil && i
}

// Run runs one line, writing what it prints to out; it reads no input.
func (s *Session) Run(ctx context.Context, line string, out io.Writer) Result {
	return s.RunIO(ctx, line, nil, out)
}

// RunIO is Run with an input: what `wctr run -i` and `wctr exec -i` send
// to the container (nil: none).
func (s *Session) RunIO(ctx context.Context, line string, in io.Reader, out io.Writer) Result {
	if in == nil {
		in = strings.NewReader("")
	}
	w := s.writer(out)
	defer w.flush()
	words, err := lineedit.Split(line)
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
		cmd := cli.RootCmd(s.CLIOptions())
		// As cmd/wctr: the root's flags are read before the subcommand,
		// so each subcommand keeps its own short flags (compose's -f is
		// --file, logs' -f is --follow).
		cmd.TraverseChildren = true
		cmd.SetIn(in)
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
  wctr run [--rm] [-d] [--name N] [-e K=V] [-w DIR] [-m 64m] [-v VOL:/path[:ro]] IMAGE [ARGS...]
  wctr ps [-a]                    list containers
  wctr logs [-f] CONTAINER        show a container's output
  wctr start [-a] CONTAINER       run a stopped container again (-a: wait, show its output)
  wctr stop | restart CONTAINER   stop (at once: wasm has no signals) / restart
  wctr kill CONTAINER             stop a running container
  wctr diff CONTAINER             the files it added (A), changed (C), deleted (D)
  wctr stats [--no-stream]        memory and writes of running containers
  wctr rm [-f] CONTAINER...       remove containers
  wctr volume create|ls|rm NAME   named volumes, shared by the containers mounting them
  wctr rmi IMAGE...               remove images
  wctr inspect NAME               details, as JSON
  wctr events                     what happens, as it happens (Ctrl-C to stop)
  wctr system df | prune -f       what is stored, and freeing it
  clear                           clear the screen
Everything is kept in this browser across reloads; the page works offline.
Tab completes commands, flags, images and container names; Tab Tab lists
them. Up/Down and Ctrl-R recall previous lines; Ctrl-A/E, Ctrl-U/K/W edit.
Ctrl-C interrupts the running command.
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
