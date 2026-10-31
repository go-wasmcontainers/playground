package mirror

import (
	"archive/tar"
	"bytes"
	"errors"
	"strings"
	"testing"
)

type ent struct {
	name, link string
	typ        byte
	size       int
}

func mkTar(t testing.TB, es ...ent) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, e := range es {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: 0o755, Linkname: e.link}
		if typ == tar.TypeReg {
			h.Size = int64(e.size)
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			tw.Write(make([]byte, e.size))
		}
	}
	tw.Close()
	return b.Bytes()
}

func TestScanTarGood(t *testing.T) {
	b := mkTar(t, ent{name: "lua.org/v5/bin/lua.wasm", size: 10}, ent{name: "./lua.org/v5/bin/luac.wasm", size: 3},
		ent{name: "lua.org/v5/lib/x.wasm", size: 1}, ent{name: "lua.org/v5/bin/link.wasm", typ: tar.TypeSymlink, link: "lua.wasm"})
	es, wasm, err := ScanTar(bytes.NewReader(b), DefaultBottleLimits)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 4 || wasm["lua"] != "/lua.org/v5/bin/lua.wasm" || wasm["luac"] != "/lua.org/v5/bin/luac.wasm" || len(wasm) != 2 {
		t.Errorf("entries %v wasm %v", es, wasm)
	}
}

func TestScanTarRefuses(t *testing.T) {
	for name, es := range map[string][]ent{
		"absolute":       {{name: "/etc/passwd", size: 1}},
		"dotdot":         {{name: "a/../../b", size: 1}},
		"dotdot-leading": {{name: "../b", size: 1}},
		"symlink-abs":    {{name: "l", typ: tar.TypeSymlink, link: "/etc/passwd"}},
		"symlink-up":     {{name: "a/l", typ: tar.TypeSymlink, link: "../../x"}},
		"hardlink-up":    {{name: "h", typ: tar.TypeLink, link: "../x"}},
		"link-empty":     {{name: "h", typ: tar.TypeSymlink}},
	} {
		_, _, err := ScanTar(bytes.NewReader(mkTar(t, es...)), DefaultBottleLimits)
		var ue *UnsafeError
		if !errors.As(err, &ue) {
			t.Errorf("%s: want UnsafeError, got %v", name, err)
		}
	}
	// A symlink that stays inside is fine.
	if _, _, err := ScanTar(bytes.NewReader(mkTar(t, ent{name: "a/b/l", typ: tar.TypeSymlink, link: "../c"})), DefaultBottleLimits); err != nil {
		t.Errorf("inside link: %v", err)
	}
}

func TestScanTarLimits(t *testing.T) {
	lim := DefaultBottleLimits
	lim.MaxUnpacked = 1 << 20
	// A bomb: one entry past the unpacked limit is an error, not a truncation.
	b := mkTar(t, ent{name: "bin/x.wasm", size: 2 << 20})
	_, _, err := ScanTar(bytes.NewReader(b), lim)
	var le *LimitError
	if !errors.As(err, &le) {
		t.Errorf("bomb: %v", err)
	}
	// Many small entries past the entry limit.
	lim = DefaultBottleLimits
	lim.MaxEntries = 3
	var es []ent
	for i := 0; i < 10; i++ {
		es = append(es, ent{name: strings.Repeat("d/", i) + "f", size: 1})
	}
	if _, _, err := ScanTar(bytes.NewReader(mkTar(t, es...)), lim); !errors.As(err, &le) {
		t.Errorf("entries: %v", err)
	}
	// Exactly at the limit passes.
	lim = DefaultBottleLimits
	b = mkTar(t, ent{name: "bin/x.wasm", size: 1000})
	lim.MaxUnpacked = int64(len(b))
	if _, _, err := ScanTar(bytes.NewReader(b), lim); err != nil {
		t.Errorf("exact: %v", err)
	}
}

func FuzzScanTar(f *testing.F) {
	f.Add(mkTar(f, ent{name: "a/bin/x.wasm", size: 4}))
	f.Add(mkTar(f, ent{name: "../x", size: 1}))
	f.Add(mkTar(f, ent{name: "l", typ: tar.TypeSymlink, link: "../../x"}))
	f.Add([]byte("not a tar"))
	f.Fuzz(func(t *testing.T, b []byte) {
		lim := DefaultBottleLimits
		lim.MaxUnpacked = 1 << 20
		es, wasm, err := ScanTar(bytes.NewReader(b), lim)
		if err != nil {
			return
		}
		for _, e := range es {
			if safeName(e) != nil {
				t.Fatalf("accepted unsafe name %q", e)
			}
		}
		for _, p := range wasm {
			if !strings.HasPrefix(p, "/") || strings.Contains(p+"/", "/../") {
				t.Fatalf("bad module path %q", p)
			}
		}
	})
}
