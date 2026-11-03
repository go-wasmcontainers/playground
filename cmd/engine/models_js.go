//go:build js

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall/js"

	"github.com/go-wasmcontainers/engine/models"
	"github.com/go-wasmcontainers/playground/internal/session"
)

// Models in the browser (wctr run --model NAME): the pinned weights come
// from Hugging Face (the engine worker's CSP allows exactly it and its CDN,
// internal/site CSPFor), are stored in OPFS, and are checked -- size, then
// sha256 -- before the container exists (models.OpenOPFS). The progress of a
// first download is written to the terminal; a refused or failed download
// leaves nothing in OPFS.

// modelStub registers one extra model for the e2e, from the worker's
// ?modelstub=NAME,SIZE,SHA256,PATH (the page forwards it on loopback hosts
// only): fetched from PATH on this origin, over plain http, still checked
// against SIZE and SHA256. It returns the stub's base URL ("" if none).
func modelStub() (name, stubURL string) {
	loc := js.Global().Get("location")
	h := loc.Get("hostname").String()
	if h != "localhost" && h != "127.0.0.1" {
		return "", ""
	}
	q, err := url.ParseQuery(strings.TrimPrefix(loc.Get("search").String(), "?"))
	if err != nil || q.Get("modelstub") == "" {
		return "", ""
	}
	f := strings.Split(q.Get("modelstub"), ",")
	if len(f) != 4 || !strings.HasPrefix(f[3], "/") {
		return "", ""
	}
	size, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return "", ""
	}
	e := models.Entry{Name: f[0], Repo: "stub/stub", Revision: strings.Repeat("0", 40), File: "stub.gguf",
		Size: size, SHA256: f[2], License: "test"}
	if e.Check() != nil {
		return "", ""
	}
	models.Registry = append(models.Registry, e)
	return e.Name, loc.Get("origin").String() + f[3]
}

// opfsModels is the engine's model source and the `models` command's cache.
type opfsModels struct {
	stubName, stubURL string
}

func (m *opfsModels) source(e models.Entry) (models.Source, error) {
	ctx := models.WithProgress(context.Background(), func(e models.Entry, done, total int64) {
		end := ""
		if done >= total {
			end = "\r\n"
		}
		post(map[string]any{"t": "write", "data": fmt.Sprintf("\rdownloading %s: %.1f / %.1f MB (%d%%)%s",
			e.Name, float64(done)/1e6, float64(total)/1e6, done*100/total, end)})
	})
	return models.OpenOPFS(ctx, e, func() (models.Source, error) {
		u, hosts := e.URL(), []string(nil)
		if e.Name == m.stubName && m.stubURL != "" {
			u, hosts = m.stubURL, []string{js.Global().Get("location").Get("hostname").String()}
		}
		post(map[string]any{"t": "write", "data": "fetching " + e.Name + " (" + strconv.FormatInt(e.Size, 10) + " bytes, sha256 checked before use)\r\n"})
		return models.NewHTTPSource(u, e.Size, hosts, &http.Client{})
	})
}

// List is the registry with the cache's state, then the unlisted files.
func (m *opfsModels) List() ([]session.Model, error) {
	cached, err := models.ListOPFS()
	if err != nil {
		return nil, err
	}
	have := map[string]models.Cached{}
	for _, c := range cached {
		have[c.File] = c
	}
	var out []session.Model
	for _, e := range models.Registry {
		c, ok := have[e.SHA256+".gguf"]
		delete(have, e.SHA256+".gguf")
		// A file of the wrong size is a partial one: it is not "cached".
		out = append(out, session.Model{Name: e.Name, Size: e.Size, Cached: ok && c.Size == e.Size, Pinned: true, License: e.License})
	}
	for _, c := range have {
		out = append(out, session.Model{Name: c.File, Size: c.Size, Cached: true})
	}
	return out, nil
}

func (m *opfsModels) Remove(name string) (int64, error) { return models.RemoveOPFS(name) }
