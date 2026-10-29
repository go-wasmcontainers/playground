package console

import (
	"io"
	"sync"
)

// maxStdin bounds the keys waiting for a container that does not read
// them; past it they are dropped, as a full terminal queue drops them.
const maxStdin = 64 << 10

// stdin is a running line's input: what the user types goes in without
// ever blocking the console, and the command reads it until the line ends.
type stdin struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
}

func newStdin() *stdin {
	s := &stdin{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// put queues p; it never blocks, and drops what does not fit.
func (s *stdin) put(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if room := maxStdin - len(s.buf); room > 0 {
		s.buf = append(s.buf, p[:min(room, len(p))]...)
	}
	s.cond.Broadcast()
}

// close ends the input: Read returns what is left, then EOF.
func (s *stdin) close() {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *stdin) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.buf) == 0 && !s.closed {
		s.cond.Wait()
	}
	if len(s.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}
