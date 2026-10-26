//go:build js

// Command engine is the playground's engine, compiled GOOS=js and run in a
// dedicated Web Worker (engine-worker.js): the image store (IndexedDB),
// the daemon and its API served in-process, the browser executor (one
// worker per container), and the session that runs the terminal's lines
// through the wctr command tree.
//
// Messages from the page: {t:"line", id, text}, {t:"interrupt", id}.
// Messages to the page: {t:"ready", version, storage}, {t:"out", id, data},
// {t:"done", id, code, clear}.
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"syscall/js"
	"time"

	"github.com/go-wasmcontainers/engine/api"
	"github.com/go-wasmcontainers/engine/api/inproc"
	"github.com/go-wasmcontainers/engine/api/server"
	"github.com/go-wasmcontainers/engine/container"
	"github.com/go-wasmcontainers/engine/daemon"
	"github.com/go-wasmcontainers/engine/image"
	"github.com/go-wasmcontainers/engine/rootfs"
	"github.com/go-wasmcontainers/engine/runtime/browser"

	"github.com/go-wasmcontainers/playground/internal/idbstore"
	"github.com/go-wasmcontainers/playground/internal/session"
)

var version = "dev"

func post(v map[string]any) { js.Global().Call("postMessage", js.ValueOf(v)) }

type out struct{ id int }

func (o out) Write(p []byte) (int, error) {
	post(map[string]any{"t": "out", "id": o.id, "data": string(p)})
	return len(p), nil
}

func main() {
	href := js.Global().Get("location").Get("href").String()
	base := href[:strings.LastIndexByte(href, '/')+1]
	v := js.Global().Get("location").Get("search").String() // ?v=<build>, kept on every asset

	storage := "IndexedDB"
	var backend image.Backend
	if b, err := idbstore.Open(256 << 20); err == nil {
		backend = b
	} else {
		storage = "memory (" + err.Error() + ")"
		backend = image.NewMemoryBackend(512 << 20)
	}
	exec := browser.New(browser.Options{WorkerURL: base + "container-worker.js" + v, DefaultMemoryLimitBytes: 256 << 20})
	lim := image.DefaultLimits
	lim.MaxImageBytes = 256 << 20
	lim.MaxBlobBytes = 128 << 20
	d, err := daemon.New(daemon.Options{
		Backend: backend,
		Limits:  lim,
		Unpack:  rootfs.Limits{MaxBytes: 512 << 20, MaxFiles: 1 << 16, MaxRatio: 200, MaxPathLen: 4096, MaxDepth: 128},
		Registry: image.RegistryOptions{
			// Every familiar name resolves to this page's own /v2 mirror.
			Mirrors: map[string]string{"docker.io": strings.TrimSuffix(base, "/")},
		},
		Containers: container.Options{
			Executor:         exec,
			MemoryLimitBytes: 256 << 20,
			Timeout:          5 * time.Minute,
			Quota:            rootfs.Quota{MaxBytes: 64 << 20, MaxFiles: 1 << 14},
			MaxLogBytes:      4 << 20,
			MaxContainers:    64,
		},
		MaxLowers: 8,
	})
	if err != nil {
		post(map[string]any{"t": "fatal", "message": err.Error()})
		return
	}
	hc := &http.Client{Transport: &inproc.Transport{Handler: server.New(d, server.Info{Version: version})}}
	sess := &session.Session{
		Version: version,
		Client:  func(string) (*api.Client, error) { return api.NewClientWith(hc, "http://engine"), nil },
		Images:  catalog(base + "catalog.json" + v),
	}

	var mu sync.Mutex
	running := map[int]context.CancelFunc{}
	js.Global().Set("onmessage", js.FuncOf(func(_ js.Value, a []js.Value) any {
		data := a[0].Get("data")
		id := data.Get("id").Int()
		switch data.Get("t").String() {
		case "line":
			text := data.Get("text").String()
			ctx, cancel := context.WithCancel(context.Background())
			mu.Lock()
			running[id] = cancel
			mu.Unlock()
			go func() {
				res := sess.Run(ctx, text, out{id})
				mu.Lock()
				delete(running, id)
				mu.Unlock()
				cancel()
				post(map[string]any{"t": "done", "id": id, "code": res.Code, "clear": res.Clear})
			}()
		case "interrupt":
			mu.Lock()
			if c := running[id]; c != nil {
				c()
			}
			mu.Unlock()
		}
		return nil
	}))
	post(map[string]any{"t": "ready", "version": version, "storage": storage})
	select {}
}

// catalog fetches the mirror's catalog.json (same origin) for help.
func catalog(url string) []session.Image {
	resp, err := http.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	var entries []struct {
		Name        string   `json:"name"`
		Tags        []string `json:"tags"`
		Description string   `json:"description"`
	}
	if json.Unmarshal(b, &entries) != nil {
		return nil
	}
	out := make([]session.Image, len(entries))
	for i, e := range entries {
		out[i] = session.Image{Name: e.Name, Tags: e.Tags, Description: e.Description}
	}
	return out
}
