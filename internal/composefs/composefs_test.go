package composefs

import (
	"io/fs"
	"strings"
	"testing"
)

func TestNamesAreRefusedBeforeAnyByteIsKept(t *testing.T) {
	var s Store
	if err := s.Import([]File{{Name: "keep.yaml", Data: []byte("x")}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".", "..", "../etc", "a/b", "/abs", ".hidden", "a\x00b", "a b", "é.yaml", strings.Repeat("a", MaxNameLen+1)} {
		if err := s.Import([]File{{Name: name, Data: []byte("y")}}); err == nil {
			t.Errorf("name %q was accepted", name)
		}
	}
	if got := s.Names(); len(got) != 1 || got[0] != "keep.yaml" {
		t.Fatalf("a refused import changed the directory: %v", got)
	}
	for _, name := range []string{".env", "compose.yaml", "docker-compose.override.yml", "a_b-c.1", strings.Repeat("a", MaxNameLen)} {
		if err := s.Import([]File{{Name: name}}); err != nil {
			t.Errorf("name %q refused: %v", name, err)
		}
	}
}

func TestBoundsNameTheBound(t *testing.T) {
	var many []File
	for i := 0; i <= MaxFiles; i++ {
		many = append(many, File{Name: "f" + string(rune('a'+i%26)) + string(rune('a'+i/26))})
	}
	big := make([]byte, MaxFileBytes+1)
	four := make([]byte, MaxFileBytes)
	cases := []struct {
		name  string
		files []File
		want  string
	}{
		{"count", many, "at most 64"},
		{"file", []File{{Name: "a", Data: big}}, "per file"},
		{"total", []File{{Name: "a", Data: four}, {Name: "b", Data: four}, {Name: "c", Data: four}, {Name: "d", Data: four}, {Name: "e", Data: []byte("x")}}, "in all"},
		{"twice", []File{{Name: "a"}, {Name: "a"}}, "twice"},
	}
	for _, c := range cases {
		var s Store
		err := s.Import(c.files)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", c.name, err, c.want)
		}
		if len(s.Names()) != 0 {
			t.Errorf("%s: kept files after a refusal", c.name)
		}
	}
}

func TestStoreIsAReadOnlyCopy(t *testing.T) {
	var s Store
	data := []byte("services: {}\n")
	if err := s.Import([]File{{Name: "compose.yaml", Data: data}}); err != nil {
		t.Fatal(err)
	}
	data[0] = 'X'
	b, err := fs.ReadFile(&s, "compose.yaml")
	if err != nil || string(b) != "services: {}\n" {
		t.Fatalf("read %q, %v", b, err)
	}
	if _, err := fs.Stat(&s, "nope"); err == nil {
		t.Fatal("a missing file exists")
	}
	s.Clear()
	if _, err := fs.Stat(&s, "compose.yaml"); err == nil {
		t.Fatal("Clear kept a file")
	}
}
