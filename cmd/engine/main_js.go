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
// The dashboard's http.* messages (package bridge) carry the engine's API
// to the canvas UI.
package main

import (
	"fmt"
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
	"github.com/go-wasmcontainers/engine/state"

	"github.com/go-wasmcontainers/dashboard/bridge"

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
	// Containers, logs and writable layers: IndexedDB too, or memory.
	var store state.Store = &state.Mem{MaxBytes: 256 << 20}
	storageInfo := func() daemon.StorageInfo {
		return daemon.StorageInfo{Kind: "memory (IndexedDB unavailable: nothing survives a reload)", Usage: -1, Quota: -1}
	}
	if b, err := idbstore.Open(256 << 20); err == nil {
		backend = b
		store = b.State()
		storageInfo = func() daemon.StorageInfo {
			u, q, p := b.Usage()
			return daemon.StorageInfo{Kind: "IndexedDB (images, containers, logs, writable layers) and Cache Storage (compiled modules)", Usage: u, Quota: q, Persistent: p}
		}
	} else {
		storage = "memory (" + err.Error() + ")"
		backend = image.NewMemoryBackend(512 << 20)
	}
	// Compiled modules through Cache Storage and compileStreaming: off by
	// default, because measured in Chromium it costs at compile time
	// (9.4 ms against 1.4 ms for a 2.8 MB module) and buys nothing at run
	// time (a CPU-bound guest 114.5 ms against 110 ms). ?codecache=1 turns
	// it on, which the e2e uses to keep measuring both.
	codeCache := ""
	if strings.Contains(js.Global().Get("location").Get("search").String(), "codecache=1") {
		codeCache = "wctr-modules"
	}
	v = "?" + strings.SplitN(strings.TrimPrefix(v, "?"), "&", 2)[0] // the build only, on assets
	exec := browser.New(browser.Options{
		WorkerURL: base + "container-worker.js" + v, DefaultMemoryLimitBytes: 256 << 20,
		// Compiled modules across reloads; the URLs are never fetched.
		CodeCache: codeCache, CodeCacheURL: base + "module-cache/",
		OnCompile: func(s browser.CompileStat) {
			post(map[string]any{"t": "compile", "source": s.Source, "bytes": s.Bytes, "ms": float64(s.Took.Microseconds()) / 1000,
				"compileMs": float64(s.Compile.Microseconds()) / 1000, "error": s.Error})
		},
	})
	lim := image.DefaultLimits
	lim.MaxImageBytes = 256 << 20
	lim.MaxBlobBytes = 128 << 20
	d, err := daemon.New(daemon.Options{
		StorageInfo:   storageInfo,
		ImagesRemoved: exec.ClearCodeCache,
		Backend:       backend,
		Limits:        lim,
		Unpack:        rootfs.Limits{MaxBytes: 512 << 20, MaxFiles: 1 << 16, MaxRatio: 200, MaxPathLen: 4096, MaxDepth: 128},
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
			Store:            store,
		},
		MaxLowers: 8,
	})
	if err != nil {
		post(map[string]any{"t": "fatal", "message": err.Error()})
		return
	}
	// What came back from the last visit, said once in the terminal.
	var notice []string
	if r := d.Restored(); r.Restored > 0 || len(r.Repaired) > 0 {
		n := fmt.Sprintf("Restored %d container(s) from your last visit", r.Restored-len(r.Removed))
		if r.Stopped > 0 {
			n += fmt.Sprintf("; %d were running and are now exited (137)", r.Stopped)
		}
		if len(r.Removed) > 0 {
			n += fmt.Sprintf("; %d --rm container(s) removed", len(r.Removed))
		}
		notice = append(notice, n+".")
		for _, m := range r.Repaired {
			notice = append(notice, "Repaired: "+m)
		}
	}
	handler := server.New(d, server.Info{Version: version})
	hc := &http.Client{Transport: &inproc.Transport{Handler: handler}}
	// The dashboard (the page's canvas UI) reaches the same API over
	// postMessage: its http.* messages go to the bridge.
	_, bridgeHandle := bridge.ServeJS(handler, js.Global())
	sess := &session.Session{
		Version: version,
		Client:  func(string) (*api.Client, error) { return api.NewClientWith(hc, "http://engine"), nil },
		Images:  session.FetchCatalog(base + "catalog.json" + v),
	}

	// The callback only copies the message and hands it on: a js.FuncOf
	// callback must never block (completion may wait on IndexedDB, which
	// needs this very event loop).
	var con *console.Console
	js.Global().Set("onmessage", js.FuncOf(func(_ js.Value, a []js.Value) any {
		data := a[0].Get("data")
		if bridgeHandle(data) {
			return nil
		}
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
				Notice:  notice,
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
	r := d.Restored()
	post(map[string]any{"t": "ready", "version": version, "storage": storage,
		"restored": r.Restored - len(r.Removed), "stopped": r.Stopped, "repaired": len(r.Repaired)})
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
