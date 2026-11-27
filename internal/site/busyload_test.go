package site

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEngineWorkerLoadsBusyLogAsAFile: the busy log wraps Go's resume loop, so
// engine-worker.js must importScripts it after wasm_exec.js and before Go
// runs, as a served file: the page's CSP has no 'unsafe-eval', so the engine
// cannot evaluate it from a string (engine#78).
func TestEngineWorkerLoadsBusyLogAsAFile(t *testing.T) {
	b, err := os.ReadFile("../../web/engine-worker.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	glue := strings.Index(src, `importScripts("wasm_exec.js" + v)`)
	busy := strings.Index(src, `importScripts("busy.js" + v)`)
	run := strings.Index(src, "go.run(")
	if glue < 0 || busy < 0 || run < 0 || !(glue < busy && busy < run) {
		t.Errorf("want wasm_exec.js, then busy.js, then go.run; at %d, %d, %d", glue, busy, run)
	}
}

// TestBuildRefusesWhenBusyLogCannotBeWritten: a site without busy.js would
// make the engine worker's importScripts throw at start, so Build fails
// instead of publishing it.
func TestBuildRefusesWhenBusyLogCannotBeWritten(t *testing.T) {
	out := filepath.Join(t.TempDir(), "site")
	real := writeServed
	t.Cleanup(func() { writeServed = real })
	var wrote []string
	writeServed = func(name string, b []byte) error {
		wrote = append(wrote, filepath.Base(name))
		if filepath.Base(name) == "busy.js" {
			if len(b) == 0 {
				t.Error("busy.js written empty")
			}
			return errors.New("disk full")
		}
		return real(name, b)
	}
	var log bytes.Buffer
	_, err := Build(context.Background(), Options{Root: "../..", Out: out, Version: "test", Log: &log})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Build with an unwritable busy.js: %v (wrote %v)", err, wrote)
	}
}
