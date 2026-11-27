package composefs

import (
	"bytes"
	"runtime"
	"strings"
	"testing"

	"github.com/go-wasmcontainers/compose"
)

// load is what `wctr compose config` does with the directory.
func load(s *Store) (*compose.Project, error) {
	return compose.Load(compose.Options{FS: s, Environment: map[string]string{}})
}

// heapLimit is the most a refusal may leave allocated. The bomb tests run
// under GOMEMLIMIT and ulimit -v in CI as well; this is the assertion that
// does not depend on them.
const heapLimit = 256 << 20

func heap() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// split turns fuzz bytes into a file set: entries end at "\x00\x00", a name
// ends at the first "\x01". More than the limit of entries are kept, so the
// bound on the count is exercised too.
func split(b []byte) []File {
	var files []File
	for _, e := range bytes.Split(b, []byte("\x00\x00")) {
		name, data, _ := bytes.Cut(e, []byte{1})
		files = append(files, File{Name: string(name), Data: data})
		if len(files) > MaxFiles+4 {
			break
		}
	}
	return files
}

// FuzzComposeImport: any set of files is refused by Import, or imported and
// then either loaded or refused by compose. Never a panic, never a heap above
// the bound.
func FuzzComposeImport(f *testing.F) {
	f.Add([]byte("compose.yaml\x01services:\n  web:\n    image: hello\n    ports: [\"8080:80\"]\n"))
	f.Add([]byte("compose.yaml\x01name: x\nservices:\n  a:\n    image: i\n    networks: [n]\nnetworks:\n  n:\n    internal: true\n\x00\x00.env\x01A=1\n"))
	f.Add([]byte("compose.yaml\x01include:\n  - other.yaml\n\x00\x00other.yaml\x01services:\n  b:\n    image: j\n"))
	f.Add([]byte("compose.yaml\x01a: &a [1,2]\nb: &b [*a,*a]\nservices: {}\n"))
	f.Add([]byte("../escape\x01x\x00\x00a/b\x01y"))
	f.Add([]byte("compose.yaml\x01services:\n  a:\n    image: ${X:-${Y:-${Z}}}\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		var s Store
		if err := s.Import(split(b)); err != nil {
			if len(s.Names()) != 0 {
				t.Fatal("a refused import kept files")
			}
			return
		}
		load(&s) // an error is an answer; a panic or an unbounded loop is not
		if h := heap(); h > heapLimit {
			t.Fatalf("heap %d MiB after a load", h>>20)
		}
	})
}

func refused(t *testing.T, files []File) {
	t.Helper()
	var s Store
	if err := s.Import(files); err != nil {
		return // refused before a byte was kept
	}
	p, err := load(&s)
	if err == nil {
		t.Fatalf("compose accepted a bomb: %d services", len(p.Services))
	}
	if h := heap(); h > heapLimit {
		t.Fatalf("refusal left %d MiB allocated", h>>20)
	}
}

func TestBombBillionLaughs(t *testing.T) {
	var y strings.Builder
	y.WriteString("x-a: &a0 [\"lol\",\"lol\",\"lol\",\"lol\",\"lol\",\"lol\",\"lol\",\"lol\",\"lol\"]\n")
	prev := "a0"
	for i := 1; i <= 12; i++ {
		cur := "a" + string(rune('0'+i%10)) + string(rune('a'+i))
		y.WriteString("x-" + cur + ": &" + cur + " [" + strings.Repeat("*"+prev+",", 9) + "*" + prev + "]\n")
		prev = cur
	}
	y.WriteString("services:\n  s:\n    image: hello\n    command: *" + prev + "\n")
	refused(t, []File{{Name: "compose.yaml", Data: []byte(y.String())}})
}

func TestBombDeepNesting(t *testing.T) {
	const depth = 1 << 20
	if depth > MaxFileBytes/2 {
		// 2^20 "[" and "]" is 2 MiB: inside the per-file bound on purpose,
		// so compose's own depth limit is what refuses it.
	}
	data := "services:\n  s:\n    image: hello\n    x: " + strings.Repeat("[", depth) + strings.Repeat("]", depth) + "\n"
	refused(t, []File{{Name: "compose.yaml", Data: []byte(data)}})
}

func TestBombTotalSize(t *testing.T) {
	chunk := bytes.Repeat([]byte("#"), MaxFileBytes)
	files := []File{{Name: "compose.yaml", Data: []byte("services: {}\n")}}
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		files = append(files, File{Name: n, Data: chunk}) // 5 x 4 MiB = 20 MiB, over 16
	}
	var s Store
	err := s.Import(files)
	if err == nil || !strings.Contains(err.Error(), "in all") {
		t.Fatalf("a 20 MiB import: %v", err)
	}
	if len(s.Names()) != 0 {
		t.Fatal("kept files of a refused import")
	}
}

func TestBombIncludeLoop(t *testing.T) {
	refused(t, []File{
		{Name: "compose.yaml", Data: []byte("include:\n  - a.yaml\n")},
		{Name: "a.yaml", Data: []byte("include:\n  - compose.yaml\n")},
	})
}

func TestEscapesAreRefused(t *testing.T) {
	for _, data := range []string{
		"include:\n  - ../../etc/passwd\n",
		"services:\n  s:\n    image: hello\n    env_file: /etc/passwd\n",
		"services:\n  s:\n    image: hello\n    volumes: [\"/:/host\"]\n",
	} {
		var s Store
		if err := s.Import([]File{{Name: "compose.yaml", Data: []byte(data)}}); err != nil {
			t.Fatal(err)
		}
		if p, err := load(&s); err == nil {
			// A bind mount is a valid model; it must not have been read.
			if !strings.Contains(data, "volumes") {
				t.Errorf("accepted %q: %v", data, p.Services)
			}
		}
	}
}
