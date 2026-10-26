package console

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-wasmcontainers/playground/internal/session"
)

type sink struct {
	mu   sync.Mutex
	out  strings.Builder
	hist []string
	done []Done
}

func (s *sink) write(x string) { s.mu.Lock(); s.out.WriteString(x); s.mu.Unlock() }
func (s *sink) text() string   { s.mu.Lock(); defer s.mu.Unlock(); return s.out.String() }
func (s *sink) dones() int     { s.mu.Lock(); defer s.mu.Unlock(); return len(s.done) }

func wait(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestConsole(t *testing.T) {
	var s sink
	c := New(Options{
		Session: &session.Session{},
		Write:   s.write,
		History: func(h []string) { s.mu.Lock(); s.hist = h; s.mu.Unlock() },
		Done:    func(d Done) { s.mu.Lock(); s.done = append(s.done, d); s.mu.Unlock() },
		Cols:    80,
		Saved:   []string{"wctr ps"},
	})
	if !strings.Contains(s.text(), "Tab completes") || !strings.HasSuffix(s.text(), "\x1b[0m\r\x1b[2C") {
		t.Fatalf("%q", s.text())
	}
	c.Input("he\t")
	wait(t, func() bool { return strings.Contains(s.text(), "help ") })
	c.Input("\r")
	wait(t, func() bool { return s.dones() == 1 })
	if !strings.Contains(s.text(), "wctr pull IMAGE") {
		t.Fatal("help not printed")
	}
	c.Input("\x1b[A") // history: help, then wctr ps before it
	c.Input("\x1b[A")
	wait(t, func() bool {
		return strings.HasSuffix(strings.TrimRight(s.text(), "\x1b[0123456789C\r"), "wctr ps") || strings.Contains(s.text(), "$ \x1b[0mwctr ps")
	})
	c.Input("\x03")
	wait(t, func() bool { return strings.Contains(s.text(), "^C") })
	c.Input("nosuch\r")
	wait(t, func() bool { return s.dones() == 2 })
	if !strings.Contains(s.text(), "[exit status 127]") {
		t.Fatalf("%q", s.text())
	}
	c.Input("\x0c")
	wait(t, func() bool { return strings.Contains(s.text(), "\x1b[H\x1b[2J") })
	c.Input("clear\r")
	wait(t, func() bool { return s.dones() == 3 })
	// Ctrl-R, Enter: the found line runs and is what stays on screen.
	c.Input("\x12osu\r")
	wait(t, func() bool { return s.dones() == 4 })
	s.mu.Lock()
	d := s.done[3]
	s.mu.Unlock()
	if d.Line != "nosuch" || !strings.Contains(s.text(), "`osu': \x1b[0mnosuch\r\x1b[31C\r\x1b[J\x1b[1;36m$ \x1b[0mnosuch\r\x1b[8C\r\n") {
		t.Fatalf("%+v %q", d, s.text())
	}
	c.Resize(40)
	s.mu.Lock()
	h := strings.Join(s.hist, ",")
	s.mu.Unlock()
	if h != "wctr ps,help ,nosuch,clear,nosuch" { // kept as typed, like bash

		t.Fatal(h)
	}
	// A full queue drops input rather than blocking a JS callback.
	for i := 0; i < 1000; i++ {
		c.Input("x")
	}
}
