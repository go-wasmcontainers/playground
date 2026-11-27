// Package composefs is the file system `wctr compose` reads in the page.
//
// A browser has no working directory and no os.Root, so compose's native
// loader fails there ("getwd: not implemented on js"). The playground hands
// compose this in-memory directory instead. Files enter it only through
// Import, which refuses, before keeping a byte, a set that is too large, too
// numerous or badly named, and replaces the whole set or none of it.
package composefs

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"sync/atomic"
	"testing/fstest"
)

// The bounds of one import. Compose applies its own (tree.DefaultLimits,
// 64 files per load) on top of these when it parses.
const (
	MaxFiles      = 64
	MaxFileBytes  = 4 << 20
	MaxTotalBytes = 16 << 20
	MaxNameLen    = 128
)

// nameRE: one path element, no separator, no "..", no NUL, no leading dot
// except .env (compose's default environment file).
var nameRE = regexp.MustCompile(`^(\.env|[A-Za-z0-9_-][A-Za-z0-9._-]*)$`)

// File is one imported file.
type File struct {
	Name string
	Data []byte
}

// Store is a flat, read-only directory that Import replaces whole. It is an
// fs.FS; the zero value is an empty directory.
type Store struct {
	cur atomic.Pointer[fstest.MapFS]
}

// Check reports why files cannot be imported, naming the bound, or nil.
// It reads only names and lengths.
func Check(files []File) error {
	if len(files) > MaxFiles {
		return fmt.Errorf("composefs: %d files; at most %d are accepted", len(files), MaxFiles)
	}
	var total int64
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		if len(f.Name) == 0 || len(f.Name) > MaxNameLen {
			return fmt.Errorf("composefs: a file name must be 1 to %d bytes, got %d", MaxNameLen, len(f.Name))
		}
		if !nameRE.MatchString(f.Name) {
			return fmt.Errorf("composefs: file name %q: only letters, digits, '.', '_' and '-' are accepted, with no leading dot (except .env)", f.Name)
		}
		if seen[f.Name] {
			return fmt.Errorf("composefs: file name %q appears twice", f.Name)
		}
		seen[f.Name] = true
		if len(f.Data) > MaxFileBytes {
			return fmt.Errorf("composefs: %s is %d bytes; at most %d per file", f.Name, len(f.Data), MaxFileBytes)
		}
		total += int64(len(f.Data))
		if total > MaxTotalBytes {
			return fmt.Errorf("composefs: more than %d bytes in all", MaxTotalBytes)
		}
	}
	return nil
}

// Import replaces the directory with files, or leaves it as it was and
// returns the refusal. The data is copied.
func (s *Store) Import(files []File) error {
	if err := Check(files); err != nil {
		return err
	}
	m := make(fstest.MapFS, len(files))
	for _, f := range files {
		m[f.Name] = &fstest.MapFile{Data: append([]byte(nil), f.Data...), Mode: 0o444}
	}
	s.cur.Store(&m)
	return nil
}

// Clear empties the directory.
func (s *Store) Clear() { s.cur.Store(nil) }

// Names lists the files, sorted.
func (s *Store) Names() []string {
	m := s.cur.Load()
	if m == nil {
		return nil
	}
	out := make([]string, 0, len(*m))
	for n := range *m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Open implements fs.FS.
func (s *Store) Open(name string) (fs.File, error) {
	m := s.cur.Load()
	if m == nil {
		return fstest.MapFS(nil).Open(name)
	}
	return m.Open(name)
}
