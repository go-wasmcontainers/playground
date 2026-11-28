//go:build js && wasm

package main

import (
	"context"
	"syscall/js"

	"github.com/go-widgets/mvvm"

	"github.com/go-wasmcontainers/dashboard/vm"

	"github.com/go-wasmcontainers/playground/internal/composefs"
	"github.com/go-wasmcontainers/playground/internal/session"
)

// composeReport reports the outcome to the Topology viewmodel, on the UI's
// queue: the project of the import, the refusals, the commands that ran.
func composeReport(app *vm.App, q *mvvm.Queue) session.ComposeReport {
	return session.ComposeReport{
		Imported: func(p string) { q.Post(func() { app.Topology.ComposeImported(p) }) },
		Failed:   func(m string) { q.Post(func() { app.Topology.ComposeFailed(m) }) },
		Done:     func(a []string, o string) { q.Post(func() { app.Topology.ComposeDone(a, o) }) },
	}
}

// connectCompose is the Go side of web/compose-io.js:
//
//   - window.__wctrComposeImport(names, texts) keeps the picked files in the
//     session's one compose FS (composefs validates names and bounds), runs
//     the `compose config` preview and returns a Promise of "" or the refusal;
//   - window.__wctrComposeHas() says whether files are already held (a new
//     import replaces them);
//   - window.__wctrComposeExport(project) returns a Promise of the imported
//     project's resolved compose text (`wctr compose -p <project> config`) and
//     rejects with the reason when there is none.
//
// The outcomes reach the Topology view (status, preview, last command).
func connectCompose(g js.Value, sess *session.Session, app *vm.App, q *mvvm.Queue) {
	rep := composeReport(app, q)
	g.Set("__wctrComposeHas", js.FuncOf(func(js.Value, []js.Value) any { return sess.HasCompose() }))
	// The page's own refusals (a file over the bound, a cancelled replace) show
	// in the same place as Go's.
	g.Set("__wctrComposeFailed", js.FuncOf(func(_ js.Value, a []js.Value) any {
		if len(a) > 0 && a[0].Type() == js.TypeString {
			rep.Failed(a[0].String())
		}
		return nil
	}))
	g.Set("__wctrComposeImport", js.FuncOf(func(_ js.Value, a []js.Value) any {
		refuse := func(msg string) any {
			rep.Failed(msg)
			return js.Global().Get("Promise").Call("resolve", msg)
		}
		if len(a) < 2 || a[0].Type() != js.TypeObject || a[1].Type() != js.TypeObject {
			return refuse("import: no files")
		}
		n := a[0].Length()
		if n != a[1].Length() || n > composefs.MaxFiles {
			return refuse("import: too many files")
		}
		files := make([]composefs.File, 0, n)
		for i := 0; i < n; i++ {
			name, text := a[0].Index(i), a[1].Index(i)
			if name.Type() != js.TypeString || text.Type() != js.TypeString {
				return refuse("import: a file is not text")
			}
			files = append(files, composefs.File{Name: name.String(), Data: []byte(text.String())})
		}
		return js.Global().Get("Promise").New(js.FuncOf(func(_ js.Value, p []js.Value) any {
			resolve := p[0]
			go func() { resolve.Invoke(sess.ImportCompose(context.Background(), files, rep)) }()
			return nil
		}))
	}))
	g.Set("__wctrComposeExport", js.FuncOf(func(_ js.Value, a []js.Value) any {
		project := ""
		if len(a) > 0 && a[0].Type() == js.TypeString {
			project = a[0].String()
		}
		return js.Global().Get("Promise").New(js.FuncOf(func(_ js.Value, p []js.Value) any {
			resolve, reject := p[0], p[1]
			go func() {
				text, err := sess.ExportCompose(context.Background(), project, rep)
				if err != nil {
					reject.Invoke(err.Error())
					return
				}
				resolve.Invoke(text)
			}()
			return nil
		}))
	}))
}
