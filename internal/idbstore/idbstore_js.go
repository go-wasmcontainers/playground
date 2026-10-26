//go:build js

// Package idbstore is an image.Backend over the browser's IndexedDB, so
// pulled images survive a page reload. Blobs are keyed by their sha256;
// the Store verified each one before PutBlob, and OpenBlob's reader is
// re-verified by the daemon when layers are unpacked.
package idbstore

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall/js"

	"github.com/go-wasmcontainers/engine/image"
)

// Backend is the IndexedDB backend. The JS side (engine-worker.js) defines
// self.idb = {open, get, put, del, keys}, each returning a Promise.
type Backend struct {
	idb js.Value
	db  js.Value
	// MaxBytes bounds what one blob may be (the browser bounds the total).
	MaxBytes int64

	mu   sync.Mutex
	keys map[string]bool
}

func await(p js.Value) (js.Value, error) {
	done := make(chan struct{})
	var res js.Value
	var err error
	then := js.FuncOf(func(_ js.Value, a []js.Value) any {
		if len(a) > 0 {
			res = a[0]
		} else {
			res = js.Undefined()
		}
		close(done)
		return nil
	})
	catch := js.FuncOf(func(_ js.Value, a []js.Value) any {
		err = errors.New(a[0].Call("toString").String())
		close(done)
		return nil
	})
	p.Call("then", then, catch)
	<-done
	then.Release()
	catch.Release()
	return res, err
}

// Open opens the database, or fails (private mode, no IndexedDB): the
// caller then falls back to memory.
func Open(maxBlob int64) (*Backend, error) {
	idb := js.Global().Get("idb")
	if idb.IsUndefined() {
		return nil, errors.New("no IndexedDB helper")
	}
	db, err := await(idb.Call("open"))
	if err != nil {
		return nil, err
	}
	b := &Backend{idb: idb, db: db, MaxBytes: maxBlob, keys: map[string]bool{}}
	ks, err := await(idb.Call("keys", db, "blobs"))
	if err != nil {
		return nil, err
	}
	for i := 0; i < ks.Length(); i++ {
		b.keys[ks.Index(i).String()] = true
	}
	return b, nil
}

func toGo(v js.Value) []byte {
	b := make([]byte, v.Get("byteLength").Int())
	js.CopyBytesToGo(b, v)
	return b
}

func toJS(b []byte) js.Value {
	u := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(u, b)
	return u
}

// OpenBlob implements image.Backend.
func (b *Backend) OpenBlob(hex string) (io.ReadCloser, error) {
	v, err := await(b.idb.Call("get", b.db, "blobs", hex))
	if err != nil {
		return nil, err
	}
	if v.IsUndefined() || v.IsNull() {
		return nil, image.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(toGo(v))), nil
}

// HasBlob implements image.Backend.
func (b *Backend) HasBlob(hex string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.keys[hex]
}

// PutBlob implements image.Backend: nothing is stored unless the whole
// verifying reader was consumed without error.
func (b *Backend) PutBlob(hex string, r io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(r, b.MaxBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > b.MaxBytes {
		return fmt.Errorf("%w: blob larger than %d bytes", image.ErrTooLarge, b.MaxBytes)
	}
	if _, err := await(b.idb.Call("put", b.db, "blobs", hex, toJS(data))); err != nil {
		return fmt.Errorf("browser storage: %v", err)
	}
	b.mu.Lock()
	b.keys[hex] = true
	b.mu.Unlock()
	return nil
}

// DeleteBlob implements image.Backend.
func (b *Backend) DeleteBlob(hex string) error {
	if _, err := await(b.idb.Call("del", b.db, "blobs", hex)); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.keys, hex)
	b.mu.Unlock()
	return nil
}

// ListBlobs implements image.Backend.
func (b *Backend) ListBlobs() ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.keys))
	for k := range b.keys {
		out = append(out, k)
	}
	return out, nil
}

// LoadRefs implements image.Backend.
func (b *Backend) LoadRefs() ([]byte, error) {
	v, err := await(b.idb.Call("get", b.db, "meta", "refs"))
	if err != nil {
		return nil, err
	}
	if v.IsUndefined() || v.IsNull() {
		return nil, nil
	}
	return toGo(v), nil
}

// SaveRefs implements image.Backend.
func (b *Backend) SaveRefs(raw []byte) error {
	_, err := await(b.idb.Call("put", b.db, "meta", "refs", toJS(raw)))
	return err
}
