//go:build js && wasm

package main

import (
	"bytes"
	"context"
	"regexp"
	"syscall/js"

	"github.com/go-wasmcontainers/playground/internal/composefs"
	"github.com/go-wasmcontainers/playground/internal/session"
)

// projectName is what a compose project may be called when the page asks for
// its export: the name reaches a shell line, so nothing else gets through.
var projectName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// connectCompose is the Go side of web/compose-io.js:
//
//   - window.__wctrComposeImport(names, texts) keeps the picked files in the
//     session's one compose FS (composefs validates names and bounds) and
//     returns "" or the refusal;
//   - window.__wctrComposeExport(project) returns a Promise of the project's
//     resolved compose text (`wctr compose -p <project> config`) and rejects
//     with the reason when there is none.
func connectCompose(g js.Value, sess *session.Session) {
	g.Set("__wctrComposeImport", js.FuncOf(func(_ js.Value, a []js.Value) any {
		if len(a) < 2 || a[0].Type() != js.TypeObject || a[1].Type() != js.TypeObject {
			return "import: no files"
		}
		n := a[0].Length()
		if n != a[1].Length() || n > composefs.MaxFiles {
			return "import: too many files"
		}
		files := make([]composefs.File, 0, n)
		for i := 0; i < n; i++ {
			name, text := a[0].Index(i), a[1].Index(i)
			if name.Type() != js.TypeString || text.Type() != js.TypeString {
				return "import: a file is not text"
			}
			files = append(files, composefs.File{Name: name.String(), Data: []byte(text.String())})
		}
		if err := sess.ComposeFiles().Import(files); err != nil {
			return err.Error()
		}
		return ""
	}))
	g.Set("__wctrComposeExport", js.FuncOf(func(_ js.Value, a []js.Value) any {
		project := ""
		if len(a) > 0 && a[0].Type() == js.TypeString {
			project = a[0].String()
		}
		return js.Global().Get("Promise").New(js.FuncOf(func(_ js.Value, p []js.Value) any {
			resolve, reject := p[0], p[1]
			go func() {
				if !projectName.MatchString(project) {
					reject.Invoke("export: not a project name")
					return
				}
				var out bytes.Buffer
				r := sess.Run(context.Background(), "wctr compose -p "+project+" config", &out)
				if r.Code != 0 || out.Len() == 0 {
					reject.Invoke("export: " + out.String())
					return
				}
				resolve.Invoke(out.String())
			}()
			return nil
		}))
	}))
}
