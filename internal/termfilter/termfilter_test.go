package termfilter

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func run(chunks ...string) string {
	var f Filter
	var out []byte
	for _, c := range chunks {
		out = append(out, f.Filter([]byte(c))...)
	}
	return string(out)
}

func TestKeeps(t *testing.T) {
	for in, want := range map[string]string{
		"hello\n":                 "hello\n",
		"tab\tcr\r\nbs\b":         "tab\tcr\r\nbs\b",
		"\x1b[31mred\x1b[0m":      "\x1b[31mred\x1b[0m",
		"\x1b[1;38;5;208mx\x1b[m": "\x1b[1;38;5;208mx\x1b[m",
		"\x1b[38:2::1:2:3mx":      "\x1b[38:2::1:2:3mx",
		"line\x1b[K":              "line\x1b[K",
		"héllo wörld ✓ 日本":        "héllo wörld ✓ 日本",
	} {
		if got := run(in); got != want {
			t.Errorf("%q: got %q", in, got)
		}
	}
}

func TestDrops(t *testing.T) {
	for in, want := range map[string]string{
		// OSC 52 clipboard write, both terminators.
		"a\x1b]52;c;ZXZpbA==\x07b":   "ab",
		"a\x1b]52;c;ZXZpbA==\x1b\\b": "ab",
		// OSC 8 hyperlink to javascript:
		"\x1b]8;;javascript:alert(1)\x1b\\click\x1b]8;;\x1b\\": "click",
		// Window title set, and the report sequences that echo back.
		"\x1b]0;pwned\x07x": "x",
		"\x1b[21tx":         "x",
		"\x1b[6nx":          "x",
		"\x1b[cx":           "x",
		"\x1b[>cx":          "x",
		"\x1b[?1049hx":      "x",
		"\x1b[2Jx":          "x",
		"\x1b[10;10Hx":      "x",
		// DCS, APC, PM, SOS.
		"\x1bP1$qm\x1b\\x": "x",
		"\x1b_apc\x1b\\x":  "x",
		"\x1b^pm\x1b\\x":   "x",
		"\x1bXsos\x1b\\x":  "x",
		// Other escapes, C0 and C1.
		"\x1bcx":             "x",
		"\x1b7x\x1b8":        "x",
		"bell\x07":           "bell",
		"\x00\x01\x7fx":      "x",
		"\u009b31mx":         "31mx", // C1 CSI as UTF-8 is dropped, its params are text
		"\u009d52;c;x\u009c": "52;c;x",
		// Invalid UTF-8 becomes U+FFFD.
		"a\xffb": "a�b",
		// A CSI with too many parameters is dropped, not passed.
		"\x1b[" + strings.Repeat("1;", 100) + "mx": "x",
		// An ESC inside a CSI restarts.
		"\x1b[3\x1b[31mx": "\x1b[31mx",
		"\x1b[?\x1b[31mx": "\x1b[31mx",
		// An unterminated string sequence ends after maxStr bytes.
		"\x1b]" + strings.Repeat("a", maxStr+10): strings.Repeat("a", 9),
		"\x1b]x\x1bzy\x07ok":                     "ok",
	} {
		if got := run(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestChunkBoundaries(t *testing.T) {
	in := "a\x1b]52;c;evil\x07b\x1b[31mc\x1b[0m é ✓"
	whole := run(in)
	for i := 1; i < len(in); i++ {
		if got := run(in[:i], in[i:]); got != whole {
			t.Fatalf("split at %d: %q vs %q", i, got, whole)
		}
	}
	// A truncated rune at the end of a stream is held, not emitted broken.
	var f Filter
	if out := f.Filter([]byte{0xe2, 0x9c}); len(out) != 0 {
		t.Fatalf("%q", out)
	}
	if out := f.Filter([]byte{0x93}); string(out) != "✓" {
		t.Fatalf("%q", out)
	}
}

// FuzzFilter: whatever goes in, what comes out is valid UTF-8, holds no
// C1 control and no escape other than SGR / erase-in-line, and never
// grows by more than the U+FFFD replacements.
func FuzzFilter(f *testing.F) {
	for _, s := range corpus() {
		f.Add(s.([]byte))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var flt Filter
		half := len(b) / 2
		out := append(flt.Filter(b[:half]), flt.Filter(b[half:])...)
		if !utf8.Valid(out) {
			t.Fatalf("invalid UTF-8 out: %q", out)
		}
		if len(out) > 3*len(b) {
			t.Fatalf("output grew: %d from %d", len(out), len(b))
		}
		for i := 0; i < len(out); i++ {
			c := out[i]
			if c < 0x20 && c != '\t' && c != '\n' && c != '\r' && c != '\b' && c != 0x1b {
				t.Fatalf("control %#x passed: %q", c, out)
			}
			if c == 0x1b {
				j := i + 2
				if j > len(out) || out[i+1] != '[' {
					t.Fatalf("bare escape passed: %q", out)
				}
				for j < len(out) && (out[j] >= '0' && out[j] <= '9' || out[j] == ';' || out[j] == ':') {
					j++
				}
				if j >= len(out) || (out[j] != 'm' && out[j] != 'K') {
					t.Fatalf("disallowed sequence passed: %q", out[i:])
				}
				i = j
			}
		}
		for _, r := range string(out) {
			if r >= 0x80 && r <= 0x9f {
				t.Fatalf("C1 %U passed", r)
			}
		}
	})
}

func corpus() []any {
	return []any{
		[]byte("\x1b]52;c;ZXZpbA==\x07"), []byte("\x1b]8;;javascript:alert(1)\x1b\\x\x1b]8;;\x1b\\"),
		[]byte("\x1b[31mred\x1b[0m"), []byte("\u009b6n"), []byte("\x1bP$q\x1b\\"), []byte("a\xff\xfeb"),
	}
}

func TestWriteFuzzCorpus(t *testing.T) {
	if os.Getenv("WRITE_FUZZ_CORPUS") != "1" {
		t.Skip("set WRITE_FUZZ_CORPUS=1 to regenerate the committed corpus")
	}
	dir := filepath.Join("testdata", "fuzz", "FuzzFilter")
	os.MkdirAll(dir, 0o755)
	for i, s := range corpus() {
		body := "go test fuzz v1\n[]byte(" + strconv.Quote(string(s.([]byte))) + ")\n"
		if err := os.WriteFile(filepath.Join(dir, "seed-"+strconv.Itoa(i)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCorpusIsCommitted(t *testing.T) {
	ents, err := os.ReadDir(filepath.Join("testdata", "fuzz", "FuzzFilter"))
	if err != nil || len(ents) == 0 {
		t.Fatalf("no committed corpus: %v", err)
	}
}

var _ = bytes.Equal
