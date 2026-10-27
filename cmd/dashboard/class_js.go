//go:build js && wasm

package main

// The dashboard's side of "Join class", as go-wasmcontainers/dashboard
// v0.5.0 wires it in its own cmd/dashboard (class_js.go, BSD-3-Clause):
// that code is in package main there, so it is carried here until the
// dashboard exports it. The page's window.__wctrClass (web/app.js) and the
// engine worker's {t:"join"}/{t:"leave"}/{t:"class"} messages are the
// protocol.

import (
	"errors"
	"syscall/js"

	"github.com/go-widgets/mvvm"

	"github.com/go-wasmcontainers/dashboard/vm"
)

type classHost struct {
	page, worker js.Value
}

func (h classHost) Relay() string {
	if r := h.page.Get("relay"); r.Type() == js.TypeString {
		return r.String()
	}
	return ""
}

func (h classHost) UseRelay(origin string) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("the page could not restart the engine with that relay")
		}
	}()
	h.page.Call("useRelay", origin)
	return nil
}

func (h classHost) Join(code, name string) error {
	h.worker.Call("postMessage", map[string]any{"t": "join", "code": code, "name": name})
	return nil
}

func (h classHost) Leave() error {
	h.worker.Call("postMessage", map[string]any{"t": "leave"})
	return nil
}

func jsStr(v js.Value, name string) string {
	if f := v.Get(name); f.Type() == js.TypeString {
		return f.String()
	}
	return ""
}

func classStatus(m js.Value) vm.ClassStatus {
	return vm.ClassStatus{State: jsStr(m, "state"), Relay: jsStr(m, "relay"), Label: jsStr(m, "label"), Domain: jsStr(m, "domain"),
		Network: jsStr(m, "network"), Subnet: jsStr(m, "subnet"), Error: jsStr(m, "error")}
}

// connectClass offers the class dialog when the page supports it, and
// follows the worker's class reports.
func connectClass(g, worker js.Value, app *vm.App, q *mvvm.Queue) {
	page := g.Get("__wctrClass")
	if page.Type() != js.TypeObject || page.Get("useRelay").Type() != js.TypeFunction {
		return
	}
	h := classHost{page: page, worker: worker}
	q.Post(func() { app.Class.SetHost(h) })
	worker.Call("addEventListener", "message", js.FuncOf(func(_ js.Value, a []js.Value) any {
		m := a[0].Get("data")
		if m.Type() == js.TypeObject && jsStr(m, "t") == "class" {
			st := classStatus(m)
			q.Post(func() { app.Class.Report(st) })
		}
		return nil
	}))
	if f := page.Get("status"); f.Type() == js.TypeFunction {
		if m := f.Invoke(); m.Type() == js.TypeObject && jsStr(m, "state") != "" {
			st := classStatus(m)
			q.Post(func() { app.Class.Report(st) })
		}
	}
}
