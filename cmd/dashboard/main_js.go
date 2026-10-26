//go:build js && wasm

// Command dashboard is the playground's main UI (GOOS=js, in the page): the
// go-wasmcontainers dashboard (github.com/go-wasmcontainers/dashboard) on
// the #screen canvas, speaking the engine's API to the engine worker the
// page created (window.__wctrEngine) through the dashboard's bridge. Its
// Terminal runs the playground's session: wctr plus help and clear, with
// the image catalogue offered to `wctr pull <Tab>`.
//
// For tests the page gets window.__dashboard.state(): the scene's Probe as
// JSON (view, control boxes in canvas pixels, rows, terminal text, ...).
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"syscall/js"

	"github.com/go-wasmcontainers/dashboard/bridge"
	"github.com/go-wasmcontainers/dashboard/engine"
	"github.com/go-wasmcontainers/dashboard/ui"
	"github.com/go-wasmcontainers/dashboard/vm"
	"github.com/go-wasmcontainers/engine/api"
	"github.com/go-wasmcontainers/engine/cli"
	"github.com/go-widgets/mvvm"
	"github.com/go-widgets/webcanvas"

	"github.com/go-wasmcontainers/playground/internal/session"
)

var version = "dev"

// sessionShell is the playground's session as the dashboard Terminal's
// shell (vm.Shell).
type sessionShell struct{ s *session.Session }

func (sh sessionShell) Run(ctx context.Context, line string, out io.Writer) (int, bool) {
	r := sh.s.Run(ctx, line, out)
	return r.Code, r.Clear
}

func main() {
	g := js.Global()
	worker := g.Get("__wctrEngine")
	if worker.IsUndefined() || worker.IsNull() {
		println("dashboard: the page has no engine worker (window.__wctrEngine)")
		return
	}
	hc := &http.Client{Transport: bridge.DialJS(worker)}
	href := g.Get("location").Get("href").String()
	base := href[:strings.LastIndexByte(href, '/')+1]
	build := "" // the page names its build (window.__wctrBuild), kept on every asset
	if b := g.Get("__wctrBuild"); b.Type() == js.TypeString {
		build = "?v=" + b.String()
	}
	sess := &session.Session{
		Version: version,
		Client:  func(string) (*api.Client, error) { return api.NewClientWith(hc, "http://engine"), nil },
		Images:  session.FetchCatalog(base + "catalog.json" + build),
	}
	opts := sess.CLIOptions()

	var scene *ui.Scene
	q := mvvm.NewQueue(func() {
		if scene != nil {
			scene.Wake()
		}
	})
	app := vm.NewApp(engine.New(hc, "http://engine"), vm.NewCLI(cli.RootCmd(opts)), q.Post)
	app.Terminal.SetShell(sessionShell{sess}, sess.Completer())
	w, h := g.Get("innerWidth").Int(), g.Get("innerHeight").Int()
	scene = ui.New(app, q, w, h)

	g.Set("__dashboard", map[string]any{
		"version": version,
		"state": js.FuncOf(func(js.Value, []js.Value) any {
			b, _ := json.Marshal(scene.Probe())
			return string(b)
		}),
	})
	go app.RunLive(context.Background(), vm.PollInterval)
	webcanvas.Run("screen", scene)
}
