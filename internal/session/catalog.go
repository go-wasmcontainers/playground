package session

import (
	"encoding/json"
	"io"
	"net/http"
)

// FetchCatalog reads the mirror's catalog.json (same origin), bounded;
// any failure is an empty catalogue (help and completion just offer less).
func FetchCatalog(url string) []Image {
	resp, err := http.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	return ParseCatalog(b)
}

// ParseCatalog reads catalog.json's entries, at most 1000.
func ParseCatalog(b []byte) []Image {
	var entries []struct {
		Name        string   `json:"name"`
		Tags        []string `json:"tags"`
		Description string   `json:"description"`
		Checked     *bool    `json:"checked"`
	}
	if json.Unmarshal(b, &entries) != nil {
		return nil
	}
	if len(entries) > 1000 {
		entries = entries[:1000]
	}
	out := make([]Image, len(entries))
	for i, e := range entries {
		d := e.Description
		// pkgx images: [checked] ran clean under the engine and wasmtime at
		// build time, [mirrored] is published as is (see the README).
		if e.Checked != nil {
			if *e.Checked {
				d += " [checked]"
			} else {
				d += " [mirrored]"
			}
		}
		out[i] = Image{Name: e.Name, Tags: e.Tags, Description: d}
	}
	return out
}
