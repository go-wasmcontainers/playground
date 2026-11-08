package site

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func pinnedOf(url string, body []byte) PinnedFile {
	s := sha256.Sum256(body)
	return PinnedFile{Path: "usr/share/quake/x", URL: url, SHA256: hex.EncodeToString(s[:]), Size: int64(len(body))}
}

func TestFetchPinned(t *testing.T) {
	good := []byte("the pinned bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good":
			w.Write(good)
		case "/tampered":
			w.Write([]byte("the pinned bytez"))
		case "/short":
			w.Write(good[:5])
		case "/long":
			w.Write(append(append([]byte(nil), good...), "extra"...))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	if b, err := FetchPinned(ctx, srv.Client(), pinnedOf(srv.URL+"/good", good)); err != nil || string(b) != string(good) {
		t.Fatalf("good: %q, %v", b, err)
	}
	for _, path := range []string{"/tampered", "/short", "/long", "/missing"} {
		var pe *ErrPinned
		_, err := FetchPinned(ctx, srv.Client(), pinnedOf(srv.URL+path, good))
		if !errors.As(err, &pe) {
			t.Errorf("%s: err = %v, want a refusal (*ErrPinned)", path, err)
		}
	}
	// Right size, wrong hash: the refusal names the sha256.
	pf := pinnedOf(srv.URL+"/good", good)
	pf.SHA256 = strings.Repeat("0", 64)
	if _, err := FetchPinned(ctx, srv.Client(), pf); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("wrong hash: %v", err)
	}
	if _, err := FetchPinned(ctx, srv.Client(), PinnedFile{URL: "http://127.0.0.1:1/x", Size: 1}); err == nil {
		t.Error("unreachable server accepted")
	}
	if _, err := FetchPinned(ctx, nil, PinnedFile{URL: "://bad"}); err == nil {
		t.Error("bad URL accepted")
	}
}

// TestQuakeImagePins: the quake image is declared GPL-3.0-or-later with its
// source, carries the pak and every licence file, and pins them all.
func TestQuakeImagePins(t *testing.T) {
	var found bool
	for _, im := range Images {
		if im.Name != "quake" {
			continue
		}
		found = true
		if !strings.Contains(im.Description, "GPL-3.0-or-later") {
			t.Error("description lacks the licence")
		}
		lic := im.Files["usr/share/doc/quake/copyright"]
		for _, want := range []string{"GPL-3.0-or-later", "https://github.com/go-quake1/engine", "https://github.com/go-quake1/freepak", "d08670904b5f6fe466135374b6c64e44a0c3a6dbee915c436f8f82bcb286652f"} {
			if !strings.Contains(lic, want) {
				t.Errorf("copyright lacks %q", want)
			}
		}
		paths := map[string]PinnedFile{}
		for _, p := range im.Pinned {
			paths[p.Path] = p
			if len(p.SHA256) != 64 || p.Size <= 0 || !strings.Contains(p.URL, "/releases/download/"+FreepakTag+"/") {
				t.Errorf("%s: not a pinned release file: %+v", p.Path, p)
			}
		}
		for _, want := range []string{"pak0.pak", "COPYING", "COPYING-GPL-2", "COPYING-LibreQuake", "CREDITS-LibreQuake", "GPL-NOTICE"} {
			if _, ok := paths["usr/share/quake/"+want]; !ok {
				t.Errorf("image lacks usr/share/quake/%s", want)
			}
		}
		if p := paths["usr/share/quake/pak0.pak"]; p.Size != 36931086 {
			t.Errorf("pak size %d", p.Size)
		}
	}
	if !found {
		t.Fatal("no quake image")
	}
}
