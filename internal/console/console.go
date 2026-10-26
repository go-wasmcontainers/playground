// Package console is the playground's terminal behind its adapter: the
// engine's line editor (cli/lineedit) and renderer, wired to a session that
// runs the submitted lines. The page only forwards what the user types and
// writes what this produces; a go-widgets terminal widget can drive the
// same editor through lineedit directly.
package console

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-wasmcontainers/engine/cli/lineedit"

	"github.com/go-wasmcontainers/playground/internal/session"
	"github.com/go-wasmcontainers/playground/internal/termfilter"
)

// Done reports a finished line (for tests and the page's transcript).
type Done struct {
	Line string
	Code int
	Took time.Duration
}

// Options wire a console.
type Options struct {
	Session *session.Session
	// Write receives terminal output (ANSI).
	Write func(string)
	// History, when set, receives the history after each submitted line.
	History func([]string)
	// Done, when set, is called when a line has finished.
	Done func(Done)
	// Cols is the terminal width.
	Cols int
	// Saved is a restored history.
	Saved []string
	// Notice lines are printed after the banner (what came back from the
	// last visit), through the terminal filter like any output.
	Notice []string
}

// Console is one terminal.
type Console struct {
	o   Options
	ed  *lineedit.Editor
	r   *lineedit.Renderer
	in  chan string
	mu  sync.Mutex
	run context.CancelFunc // set while a line runs
}

// New starts a console; it prints the banner and the prompt.
func New(o Options) *Console {
	c := &Console{o: o, in: make(chan string, 64)}
	c.ed = lineedit.New(lineedit.Options{Complete: o.Session.Completer(), Cols: o.Cols})
	c.ed.SetHistory(o.Saved)
	c.r = &lineedit.Renderer{Cols: o.Cols}
	o.Write("\x1b[1mwctr playground\x1b[0m: WebAssembly containers, in this tab. Type \x1b[1mhelp\x1b[0m; Tab completes.\r\n")
	for _, n := range o.Notice {
		var f termfilter.Filter
		o.Write("\x1b[2m" + string(f.Filter([]byte(strings.ReplaceAll(n, "\n", " ")))) + "\x1b[0m\r\n")
	}
	o.Write(c.r.Render(c.ed.State()))
	go c.loop()
	return c
}

// Input queues what the user typed. It never blocks the caller (a
// JavaScript callback must not): keys past a full queue are dropped.
func (c *Console) Input(data string) {
	select {
	case c.in <- data:
	default:
	}
}

// Resize sets the terminal width.
func (c *Console) Resize(cols int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ed.SetCols(cols)
	c.r.Cols = cols
}

func (c *Console) loop() {
	for data := range c.in {
		for _, k := range lineedit.ParseKeys(data) {
			c.key(k)
		}
	}
}

func (c *Console) key(k lineedit.Key) {
	c.mu.Lock()
	if c.run != nil {
		// No stdin for containers yet: only Ctrl-C reaches a running line.
		if k.Code == lineedit.KeyCtrlC {
			c.run()
			c.o.Write("^C")
		}
		c.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	before := c.ed.State()
	ev := c.ed.Key(ctx, k)
	cancel()
	switch {
	case ev.Submit:
		// Redraw the accepted line (it may come from a Ctrl-R search,
		// whose prompt was on screen), then leave it.
		s := lineedit.State{Prompt: c.ed.State().Prompt, Line: ev.Line, Cursor: utf8.RuneCountInString(ev.Line)}
		c.o.Write(c.r.Render(s) + c.r.Leave(s))
		if c.o.History != nil {
			c.o.History(c.ed.History())
		}
		c.start(ev.Line)
	case ev.Interrupt:
		c.o.Write("^C" + c.r.Leave(before) + c.r.Render(c.ed.State()))
	case ev.Clear:
		c.r.Forget()
		c.o.Write("\x1b[H\x1b[2J" + c.r.Render(c.ed.State()))
	case ev.Print != nil:
		c.o.Write(c.r.Print(ev.Print, c.ed.State()))
	default:
		c.o.Write(c.r.Render(c.ed.State()))
	}
	c.mu.Unlock()
}

// start runs a line; called with c.mu held.
func (c *Console) start(line string) {
	ctx, cancel := context.WithCancel(context.Background())
	c.run = cancel
	go func() {
		t0 := time.Now()
		res := c.o.Session.Run(ctx, line, writer{c.o.Write})
		cancel()
		c.mu.Lock()
		c.run = nil
		if res.Clear {
			c.r.Forget()
			c.o.Write("\x1b[H\x1b[2J")
		} else if res.Code != 0 {
			c.o.Write(fmt.Sprintf("\x1b[2m[exit status %d]\x1b[0m\r\n", res.Code))
		}
		c.o.Write(c.r.Render(c.ed.State()))
		c.mu.Unlock()
		if c.o.Done != nil {
			c.o.Done(Done{Line: line, Code: res.Code, Took: time.Since(t0)})
		}
	}()
}

type writer struct{ f func(string) }

func (w writer) Write(p []byte) (int, error) {
	w.f(string(p))
	return len(p), nil
}
