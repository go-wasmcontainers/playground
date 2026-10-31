//go:build js

package main

import (
	"context"
	"syscall/js"

	"github.com/go-wasmcontainers/playground/internal/portrelay"
	"github.com/go-wasmcontainers/playground/internal/portroute"
)

// Published ports in the browser (`wctr run -p`): the page asks for a
// session on a port and opens the view tab with it; the view relays the
// requests of the container's page (from the usercontent shell) here, and
// the answers go back the same way (internal/portrelay).
//
// Messages from the page:
//
//	{t:"ports", rid}                             the published ports
//	{t:"port-open", rid, port, usercontent}      a session for one port
//	{t:"port-req", sid, uc, id, port, method, path, headers, body}
//	{t:"port-abort", sid, id}                    the shell gave the request up
//	{t:"port-close", sid}                        the view tab went away
//
// Messages to the page: {t:"ports", rid, items}, {t:"port-opened", rid,
// sid, nonce, port, usercontent, error}, and per request {t:"port-head",
// sid, id, status, headers}, {t:"port-chunk", sid, id, data} (an
// ArrayBuffer, transferred), {t:"port-end", sid, id}, {t:"port-error", sid,
// id, status, message}.

// portMessage handles a port message, and says whether data was one.
func portMessage(relay *portrelay.Relay, published func() []uint16, data js.Value) bool {
	switch data.Get("t").String() {
	case "ports":
		var items []any
		for _, p := range published() {
			items = append(items, int(p))
		}
		post(map[string]any{"t": "ports", "rid": str(data, "rid"), "items": items})
	case "port-open":
		rid, uc := str(data, "rid"), str(data, "usercontent")
		port := data.Get("port")
		if port.Type() != js.TypeNumber || port.Int() < 1 || port.Int() > 65535 {
			post(map[string]any{"t": "port-opened", "rid": rid, "error": "a port is a number from 1 to 65535"})
			break
		}
		sid, nonce, err := relay.Open(uint16(port.Int()), uc)
		m := map[string]any{"t": "port-opened", "rid": rid, "port": port.Int()}
		if err != nil {
			m["error"] = err.Error()
		} else {
			o, _ := portrelay.Origin(uc)
			m["sid"], m["nonce"], m["usercontent"] = sid, nonce, o
		}
		post(m)
	case "port-req":
		req, ok := portRequest(data)
		if !ok {
			post(map[string]any{"t": "port-error", "sid": str(data, "sid"), "id": str(data, "id"), "status": 400, "message": "malformed request"})
			break
		}
		// Serve waits on the container, through this event loop: never in
		// the message callback itself.
		go relay.Serve(context.Background(), req.sid, req.uc, req.id, req.port, req.method, req.path, req.header, req.body,
			pageAnswer{sid: req.sid, id: req.id})
	case "port-abort":
		relay.Abort(str(data, "sid"), str(data, "id"))
	case "port-close":
		relay.Close(str(data, "sid"))
	default:
		return false
	}
	return true
}

type portReq struct {
	sid, uc, id, method, path string
	port                      int
	header                    [][2]string
	body                      []byte
}

// portRequest reads a port-req message, bounded: what portroute checks is
// checked there, this only refuses what cannot even be read.
func portRequest(data js.Value) (portReq, bool) {
	r := portReq{sid: str(data, "sid"), uc: str(data, "uc"), id: str(data, "id"), method: str(data, "method")}
	path := data.Get("path")
	port := data.Get("port")
	if path.Type() != js.TypeString || port.Type() != js.TypeNumber {
		return r, false
	}
	if r.path = path.String(); len(r.path) > portroute.MaxPath {
		return r, false
	}
	r.port = port.Int()
	if hs := data.Get("headers"); hs.Type() == js.TypeObject {
		n := hs.Length()
		if n > portroute.MaxHeaders {
			return r, false
		}
		for i := 0; i < n; i++ {
			kv := hs.Index(i)
			if kv.Type() != js.TypeObject || kv.Length() != 2 || kv.Index(0).Type() != js.TypeString || kv.Index(1).Type() != js.TypeString {
				return r, false
			}
			r.header = append(r.header, [2]string{kv.Index(0).String(), kv.Index(1).String()})
		}
	}
	if b := data.Get("body"); b.Type() == js.TypeObject {
		u8 := js.Global().Get("Uint8Array").New(b) // an ArrayBuffer or a view of one
		n := u8.Length()
		if n > portroute.MaxBody {
			return r, false
		}
		r.body = make([]byte, n)
		js.CopyBytesToGo(r.body, u8)
	}
	return r, true
}

// pageAnswer posts a relayed answer to the page.
type pageAnswer struct{ sid, id string }

func (a pageAnswer) Head(status int, header [][2]string) {
	hs := make([]any, len(header))
	for i, kv := range header {
		hs[i] = []any{kv[0], kv[1]}
	}
	post(map[string]any{"t": "port-head", "sid": a.sid, "id": a.id, "status": status, "headers": hs})
}

func (a pageAnswer) Chunk(b []byte) {
	u8 := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(u8, b)
	buf := u8.Get("buffer")
	m := js.ValueOf(map[string]any{"t": "port-chunk", "sid": a.sid, "id": a.id})
	m.Set("data", buf)
	js.Global().Call("postMessage", m, js.ValueOf([]any{buf}))
}

func (a pageAnswer) End() { post(map[string]any{"t": "port-end", "sid": a.sid, "id": a.id}) }

func (a pageAnswer) Fail(status int, msg string) {
	post(map[string]any{"t": "port-error", "sid": a.sid, "id": a.id, "status": status, "message": msg})
}
