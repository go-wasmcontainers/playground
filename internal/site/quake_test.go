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
	if _, err := FetchPinned(ctx, nil, PinnedFile{URL: "://bad"}); err == nil || !strings.Contains(err.Error(), "missing protocol scheme") {
		t.Errorf("bad URL: %v", err)
	}
}

// Each refusal of FetchPinned is held by its own check: a status that is not
// 200 is refused whatever the body, a body that stops short is a transport
// error (never a pin mismatch), and a size that differs is refused even when
// the hash of the bytes read is the pinned one.
func TestFetchPinnedEachGuard(t *testing.T) {
	good := []byte("the pinned bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/404-right-body":
			w.WriteHeader(404)
			w.Write(good)
		case "/cut":
			w.Header().Set("Content-Length", "1000")
			w.Write(good[:3])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		case "/long":
			w.Write(append(append([]byte(nil), good...), "extra"...))
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	var pe *ErrPinned
	_, err := FetchPinned(ctx, srv.Client(), pinnedOf(srv.URL+"/404-right-body", good))
	if !errors.As(err, &pe) || !strings.Contains(pe.Why, "HTTP 404") {
		t.Errorf("404 with the pinned bytes: %v", err)
	}

	_, err = FetchPinned(ctx, srv.Client(), pinnedOf(srv.URL+"/cut", good))
	if err == nil || errors.As(err, &pe) {
		t.Errorf("a body cut short: %v, want a transport error", err)
	}

	// The pinned hash is that of the first len(good)+1 bytes, which are what
	// FetchPinned reads of a longer body: only the size check refuses it.
	pf := pinnedOf(srv.URL+"/long", good)
	s := sha256.Sum256(append(append([]byte(nil), good...), 'e'))
	pf.SHA256 = hex.EncodeToString(s[:])
	_, err = FetchPinned(ctx, srv.Client(), pf)
	if !errors.As(err, &pe) || !strings.Contains(pe.Why, "pinned") || strings.Contains(pe.Why, "sha256") {
		t.Errorf("longer than pinned, hash of what was read right: %v", err)
	}

	if _, err := FetchPinned(ctx, srv.Client(), PinnedFile{URL: "http://127.0.0.1:1/x", Size: 1}); err == nil || errors.As(err, &pe) {
		t.Errorf("unreachable server: %v, want a transport error", err)
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
		for _, want := range []string{"GPL-3.0-or-later", "https://github.com/go-quake1/engine", "https://github.com/go-quake1/freepak", "924c2f93ac43b085fbeb7ccc6f4fdecbca6782e3eea35bbfa0f995669fdc2ed8"} {
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
		if p := paths["usr/share/quake/pak0.pak"]; p.Size != 36931122 {
			t.Errorf("pak size %d", p.Size)
		}
	}
	if !found {
		t.Fatal("no quake image")
	}
}
