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
	// The bound is on what is BUFFERED, not on the total: when the reader
	// has drained some bytes before this put, "\x04" fits and is kept.
	// Main's CI read 65545 = 8 + maxStdin + 1 that way (2026-11-08).
	s.put("\x04")
	time.Sleep(10 * time.Millisecond)
	s.close()
	s.put("after close")
	select {
	case b := <-got:
		body := strings.TrimSuffix(strings.TrimPrefix(b, "echo hi\r"), "\x04")
		if !strings.HasPrefix(b, "echo hi\r") || strings.Trim(body, "x") != "" ||
			len(b) > len("echo hi\r")+maxStdin+len("\x04") || strings.Contains(b, "after") {
			t.Fatalf("read %d bytes, starting %q, ending %q", len(b), b[:min(len(b), 16)], b[max(0, len(b)-4):])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader not released by close")
	}
}
