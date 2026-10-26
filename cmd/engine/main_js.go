//go:build js

// Command engine is the playground's engine, compiled GOOS=js and run in a
// dedicated Web Worker (engine-worker.js): the image store (IndexedDB),
// the daemon and its API served in-process, the browser executor (one
// worker per container), and the session that runs the terminal's lines
// through the wctr command tree, behind the console: the engine's line
// editor (cli/lineedit), so editing, history and Tab completion are Go.
//
// Messages from the page: {t:"init", history, cols}, {t:"input", data}
// (what the terminal received from the keyboard, raw), {t:"resize", cols}.
// Messages to the page: {t:"ready", version, storage}, {t:"write", data}
// (ANSI for the terminal), {t:"history", items}, {t:"done", line, code, ms}.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
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

	"github.com/go-wasmcontainers/playground/internal/console"
	"github.com/go-wasmcontainers/playground/internal/idbstore"
	"github.com/go-wasmcontainers/playground/internal/session"
)

var version = "dev"

func post(v map[string]any) { js.Global().Call("postMessage", js.ValueOf(v)) }

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

	// The callback only copies the message and hands it on: a js.FuncOf
	// callback must never block (completion may wait on IndexedDB, which
	// needs this very event loop).
	var con *console.Console
	js.Global().Set("onmessage", js.FuncOf(func(_ js.Value, a []js.Value) any {
		data := a[0].Get("data")
		switch data.Get("t").String() {
		case "init":
			if con != nil {
				break
			}
			var saved []string
			if h := data.Get("history"); h.Type() == js.TypeObject {
				for i := 0; i < h.Length() && i < 1000; i++ {
					if s := h.Index(i); s.Type() == js.TypeString {
						saved = append(saved, s.String())
					}
				}
			}
			con = console.New(console.Options{
				Session: sess,
				Cols:    cols(data),
				Saved:   saved,
				Write:   func(s string) { post(map[string]any{"t": "write", "data": s}) },
				History: func(h []string) {
					items := make([]any, len(h))
					for i, s := range h {
						items[i] = s
					}
					post(map[string]any{"t": "history", "items": items})
				},
				Done: func(d console.Done) {
					post(map[string]any{"t": "done", "line": d.Line, "code": d.Code, "ms": float64(d.Took.Microseconds()) / 1000})
				},
			})
		case "input":
			if con != nil {
				con.Input(data.Get("data").String())
			}
		case "resize":
			if con != nil {
				n := cols(data)
				go con.Resize(n)
			}
		}
		return nil
	}))
	post(map[string]any{"t": "ready", "version": version, "storage": storage})
	select {}
}

// cols reads a terminal width, bounded.
func cols(data js.Value) int {
	c := data.Get("cols")
	if c.Type() != js.TypeNumber {
		return 80
	}
	n := c.Int()
	if n < 10 || n > 1000 {
		return 80
	}
	return n
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
