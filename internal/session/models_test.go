package session

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeCache struct{ rows []Model }

func (f *fakeCache) List() ([]Model, error) { return f.rows, nil }
func (f *fakeCache) Remove(name string) (int64, error) {
	for i, m := range f.rows {
		if m.Name == name && m.Cached {
			f.rows[i].Cached = false
			return m.Size, nil
		}
	}
	return 0, errors.New(name + " is not cached")
}

func TestModelsCommand(t *testing.T) {
	run := func(s *Session, line string) (string, int) {
		var b strings.Builder
		r := s.Run(context.Background(), line, &b)
		return b.String(), r.Code
	}
	if out, code := run(&Session{}, "models"); code != 1 || !strings.Contains(out, "no model cache") {
		t.Fatalf("%d %q", code, out)
	}
	f := &fakeCache{rows: []Model{
		{Name: "smollm2-135m-instruct-q4_0", Size: 91893088, Cached: true, Pinned: true},
		{Name: "other", Size: 10, Pinned: true},
	}}
	s := &Session{Models: f}
	out, code := run(s, "models ls")
	if code != 0 || !strings.Contains(out, "91.9 MB  cached") || !strings.Contains(out, "not downloaded (wctr run --model other") {
		t.Fatalf("%d %q", code, out)
	}
	if out, code := run(s, "models rm smollm2-135m-instruct-q4_0"); code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("%d %q", code, out)
	}
	if out, code := run(s, "models rm smollm2-135m-instruct-q4_0"); code != 1 || !strings.Contains(out, "not cached") {
		t.Fatalf("%d %q", code, out)
	}
	if _, code := run(s, "models rm"); code != 2 {
		t.Fatalf("usage code %d", code)
	}
}
