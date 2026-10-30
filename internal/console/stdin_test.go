package console

import (
	"io"
	"strings"
	"testing"
	"time"
)

// TestStdin: input is read in order, put never blocks and drops past the
// bound, and close lets the reader drain what is left, then see EOF.
func TestStdin(t *testing.T) {
	s := newStdin()
	got := make(chan string)
	go func() {
		b, _ := io.ReadAll(s)
		got <- string(b)
	}()
	s.put("echo hi\r")
	s.put(strings.Repeat("x", maxStdin+10)) // must not block
	s.put("\x04")                           // dropped: the buffer may be full
	time.Sleep(10 * time.Millisecond)
	s.close()
	s.put("after close")
	select {
	case b := <-got:
		if !strings.HasPrefix(b, "echo hi\r") || len(b) > maxStdin+len("echo hi\r") || strings.Contains(b, "after") {
			t.Fatalf("read %d bytes, starting %q", len(b), b[:min(len(b), 16)])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader not released by close")
	}
}
