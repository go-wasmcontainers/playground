package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-wasmcontainers/engine/cli/lineedit"
	"github.com/go-wasmcontainers/engine/container"
)

// TestCompleter types into the engine's line editor with this session's
// completer, against a real engine: the playground's Tab, natively.
func TestCompleter(t *testing.T) {
	s := engine(t, container.Options{Timeout: 3 * time.Second})
	s.Images = []Image{{Name: "hello", Description: "greets"}, {Name: "lab", Description: "toolbox"}}
	for _, l := range []string{"wctr pull lab", "wctr run --name first lab echo"} {
		if _, r := line(t, s, l); r.Code != 0 {
			t.Fatal(l)
		}
	}
	e := lineedit.New(lineedit.Options{Complete: s.Completer()})
	ctx := context.Background()
	typ := func(in string) lineedit.Event {
		var ev lineedit.Event
		for _, k := range lineedit.ParseKeys(in) {
			ev = e.Key(ctx, k)
		}
		return ev
	}
	for _, c := range []struct{ in, want string }{
		{"wc\t", "wctr "},
		// lab and lab:latest: the common prefix, no space yet.
		{"run --rm la\t", "wctr run --rm lab"},
		{" ec\t", "wctr run --rm lab echo "},
	} {
		t0 := time.Now()
		typ(c.in)
		if got := e.State().Line; got != c.want {
			t.Fatalf("%q: %q, want %q", c.in, got, c.want)
		}
		if d := time.Since(t0); d > 20*time.Millisecond {
			t.Errorf("%q took %v", c.in, d)
		}
	}
	e.Reset()
	typ("wctr run lab e\t")
	ev := typ("\t")
	if !strings.Contains(strings.Join(ev.Print, "\n"), "echo  -- print the arguments") {
		t.Fatalf("%q", ev.Print)
	}
	e.Reset()
	typ("wctr logs fi\t") // not "f": a hex ID may begin with f
	if e.State().Line != "wctr logs first " {
		t.Fatal(e.State().Line)
	}
	e.Reset()
	typ("wctr pull h\t")
	if e.State().Line != "wctr pull hello " {
		t.Fatal(e.State().Line)
	}
}
