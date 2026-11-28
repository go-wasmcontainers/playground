//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/go-widgets/mvvm"

	"github.com/go-wasmcontainers/dashboard/vm"
)

// connectSoundAll makes the page-wide mute one state. The dashboard's
// Sound.All is the owner: the page strip's "mute all" (web/audio.js) calls
// window.__wctrSoundAll(muted), which sets Sound.All, and Sound.All reaches the
// audio graph through wctrAudio.setGlobalMute (host.ConnectTopology). So the
// strip, the Topology button and the graph can never disagree.
func connectSoundAll(g js.Value, app *vm.App, q *mvvm.Queue) {
	g.Set("__wctrSoundAll", js.FuncOf(func(_ js.Value, a []js.Value) any {
		if len(a) == 0 || a[0].Type() != js.TypeBoolean {
			return nil
		}
		muted := a[0].Bool()
		q.Post(func() { app.Sound.SetAllMuted(muted) })
		return nil
	}))
}
