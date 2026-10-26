// Package termfilter makes untrusted output safe to hand to a terminal
// emulator (xterm.js in the playground).
//
// A container's output is attacker-controlled bytes, and a terminal acts
// on some of them: OSC 52 writes the clipboard, OSC 8 makes links (to
// javascript: URLs too), title and status reports are answered by the
// terminal as if the user had typed them, DCS and friends carry payloads.
// The filter keeps text, the control characters a program needs to lay
// out lines (\t \n \r \b), and the one family of sequences that only
// colours text: SGR (ESC [ params m), plus erase-in-line (ESC [ params K).
// Everything else is dropped whole, including C1 controls written as
// UTF-8 (U+0080..U+009F, which xterm.js would also obey) and invalid
// UTF-8 (replaced by U+FFFD).
package termfilter

import (
	"unicode/utf8"
)

type state int

const (
	ground state = iota
	esc          // after ESC
	csi          // ESC [ ... collecting
	csiBad       // inside a CSI we will drop
	str          // OSC / DCS / APC / PM / SOS body, dropped
	strEsc       // ESC inside a string (maybe ST)
)

// maxSeq bounds a kept CSI; maxStr bounds how long a string sequence is
// swallowed before the filter gives up and returns to text.
const (
	maxSeq = 64
	maxStr = 4096
)

// Filter is a streaming filter; feed it chunks with Write-like calls.
type Filter struct {
	st      state
	seq     []byte
	strLen  int
	pending []byte // an incomplete UTF-8 sequence carried to the next chunk
}

// Filter returns the safe part of p, carrying any incomplete sequence over
// to the next call.
func (f *Filter) Filter(p []byte) []byte {
	out := make([]byte, 0, len(p))
	if len(f.pending) > 0 {
		p = append(append([]byte(nil), f.pending...), p...)
		f.pending = nil
	}
	for i := 0; i < len(p); {
		c := p[i]
		switch f.st {
		case ground:
			switch {
			case c == 0x1b:
				f.st = esc
				i++
			case c == '\t' || c == '\n' || c == '\r' || c == '\b':
				out = append(out, c)
				i++
			case c < 0x20 || c == 0x7f:
				i++ // other C0 controls (BEL, etc.) are dropped
			case c < 0x80:
				out = append(out, c)
				i++
			default:
				r, n := utf8.DecodeRune(p[i:])
				if r == utf8.RuneError && n <= 1 {
					if !utf8.FullRune(p[i:]) {
						f.pending = append(f.pending, p[i:]...)
						return out
					}
					out = append(out, "�"...)
					i++
					continue
				}
				i += n
				if r >= 0x80 && r <= 0x9f {
					continue // C1 controls
				}
				out = utf8.AppendRune(out, r)
			}
		case esc:
			i++
			switch c {
			case '[':
				f.st = csi
				f.seq = append(f.seq[:0], 0x1b, '[')
			case ']', 'P', '_', '^', 'X':
				f.st = str
				f.strLen = 0
			default:
				f.st = ground // any other escape is dropped with its final byte
			}
		case csi:
			i++
			switch {
			case c >= '0' && c <= '9' || c == ';' || c == ':':
				if len(f.seq) >= maxSeq {
					f.st = csiBad
					continue
				}
				f.seq = append(f.seq, c)
			case c == 'm' || c == 'K':
				out = append(out, f.seq...)
				out = append(out, c)
				f.st = ground
			case c >= 0x40 && c <= 0x7e:
				f.st = ground // a final byte we do not allow
			case c == 0x1b:
				f.st = esc
			default:
				f.st = csiBad // private markers (?, >, =) or intermediates
			}
		case csiBad:
			i++
			if c >= 0x40 && c <= 0x7e {
				f.st = ground
			} else if c == 0x1b {
				f.st = esc
			}
		case str:
			i++
			f.strLen++
			switch {
			case c == 0x07:
				f.st = ground
			case c == 0x1b:
				f.st = strEsc
			case f.strLen > maxStr:
				f.st = ground
			}
		case strEsc:
			i++
			if c == '\\' {
				f.st = ground
			} else {
				f.st = str
			}
		}
	}
	return out
}
