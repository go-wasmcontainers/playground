// Package portroute is the policy of the HTTP route from a page to a
// container's published port (`wctr run -p` in a browser): what a request
// relayed from the usercontent origin may carry, what a container's answer
// may say to the browser, and where its redirects lead.
//
// The route is: the view tab (the playground's origin) embeds the shell of
// the usercontent origin (<org>-usercontent.github.io), whose service
// worker turns each request of the container's page into a message; the
// view relays it to the engine worker, which sends it to the container
// through portmap.Virtual and relays the answer back. Everything that
// arrives here came from a page the container controls: it is data,
// checked and bounded, never trusted.
package portroute

import (
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Bounds of one relayed request and its answer.
const (
	MaxPath        = 8 << 10   // the path and query
	MaxHeaders     = 100       // request header fields
	MaxHeaderBytes = 64 << 10  // request header names and values together
	MaxBody        = 16 << 20  // a request body
	MaxResponse    = 256 << 20 // a response body
)

// Methods are the methods relayed: HTTP's, and WebDAV's (RFC 4918 §9), so
// that a WebDAV server in a container -- go-fileshare's, say -- has a client
// in its own page. Each reaches only that container's port, as GET does.
var Methods = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "OPTIONS": true,
	"PROPFIND": true, "PROPPATCH": true, "MKCOL": true, "COPY": true, "MOVE": true, "LOCK": true, "UNLOCK": true}

// ErrRefused is a request the route does not relay.
var ErrRefused = errors.New("refused")

// sidRE is a session id: random, URL-safe base64, in the usercontent paths.
var sidRE = regexp.MustCompile(`^[A-Za-z0-9_-]{22,64}$`)

// ValidID reports a session id, a nonce or a request id of the route.
func ValidID(s string) bool { return sidRE.MatchString(s) }

// hopByHop are connection-specific fields (RFC 9110 §7.6.1) and the proxy
// ones: never relayed in either direction.
var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Transfer-Encoding": true, "Upgrade": true, "Te": true,
	"Trailer": true, "Proxy-Authorization": true, "Proxy-Authenticate": true, "Proxy-Connection": true,
}

// Request is a relayed request, checked.
type Request struct {
	Port   uint16
	Method string
	Path   string // begins with "/"
	Header http.Header
	Body   []byte
}

// CheckRequest validates what the shell relayed and builds the request the
// container receives: Host is localhost:<port>, the X-Forwarded-* fields
// say where the browser sees it (so an application can build its own
// absolute URLs), and the cookies are the route's own (see Jar), never
// anything the page sent.
func CheckRequest(port int, method, path string, header [][2]string, body []byte, sid, usercontent string) (*Request, error) {
	switch {
	case port < 1 || port > 65535:
		return nil, fmt.Errorf("%w: port %d", ErrRefused, port)
	case !Methods[method]:
		return nil, fmt.Errorf("%w: method %q", ErrRefused, method)
	case len(path) == 0 || path[0] != '/' || len(path) > MaxPath || strings.ContainsAny(path, "\r\n\x00"):
		return nil, fmt.Errorf("%w: path", ErrRefused)
	case len(header) > MaxHeaders:
		return nil, fmt.Errorf("%w: %d header fields, at most %d", ErrRefused, len(header), MaxHeaders)
	case len(body) > MaxBody:
		return nil, fmt.Errorf("%w: a body of %d bytes, at most %d", ErrRefused, len(body), MaxBody)
	case !ValidID(sid):
		return nil, fmt.Errorf("%w: session", ErrRefused)
	}
	u, err := url.Parse(usercontent)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%w: usercontent origin %q", ErrRefused, usercontent)
	}
	h := http.Header{}
	size := 0
	for _, kv := range header {
		name := textproto.CanonicalMIMEHeaderKey(kv[0])
		size += len(kv[0]) + len(kv[1])
		if size > MaxHeaderBytes {
			return nil, fmt.Errorf("%w: header fields larger than %d bytes", ErrRefused, MaxHeaderBytes)
		}
		if !validFieldName(kv[0]) || strings.ContainsAny(kv[1], "\r\n\x00") {
			return nil, fmt.Errorf("%w: header field %q", ErrRefused, kv[0])
		}
		if hopByHop[name] || name == "Host" || name == "Cookie" || strings.HasPrefix(name, "X-Forwarded-") || name == "Forwarded" {
			continue
		}
		h.Add(name, kv[1])
	}
	h.Set("X-Forwarded-Host", u.Host)
	h.Set("X-Forwarded-Proto", u.Scheme)
	h.Set("X-Forwarded-Prefix", Prefix(sid, uint16(port)))
	return &Request{Port: uint16(port), Method: method, Path: path, Header: h, Body: body}, nil
}

// validFieldName is RFC 9110's token.
func validFieldName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c >= 0x7f || strings.IndexByte(`"(),/:;<=>?@[\]{}`, c) >= 0 {
			return false
		}
	}
	return true
}

// Prefix is where a port's pages live on the usercontent origin.
func Prefix(sid string, port uint16) string {
	return "/s/" + sid + "/" + strconv.Itoa(int(port))
}

// dropped are response fields the browser must not get from a container:
//
//   - Set-Cookie: kept by the route's jar (a service worker cannot set a
//     cookie anyway, and the jar is what makes sessions work);
//   - Clear-Site-Data: it would wipe the usercontent origin, the shell's
//     service worker included, for every port of the session;
//   - Service-Worker-Allowed: a container's script must not claim the
//     whole origin's scope;
//   - Content-Security-Policy-Report-Only, Permissions-Policy,
//     Cross-Origin-Opener-Policy, Refresh, Alt-Svc, Strict-Transport-Security,
//     Public-Key-Pins: policies of the origin, which is not the container's;
//   - Content-Length: the route streams; the browser counts.
var dropped = map[string]bool{
	"Set-Cookie": true, "Set-Cookie2": true, "Clear-Site-Data": true, "Service-Worker-Allowed": true,
	"Content-Security-Policy-Report-Only": true, "Permissions-Policy": true, "Feature-Policy": true,
	"Cross-Origin-Opener-Policy": true, "Refresh": true, "Alt-Svc": true, "Strict-Transport-Security": true,
	"Public-Key-Pins": true, "Content-Length": true,
}

// Response filters the response header of the container on port for the
// browser, and rewrites a Location that points at a container (see
// LocationFor). Every answer gets nosniff, no-referrer and no-store:
// nothing of it is cached, by the browser or the shell.
func Response(h http.Header, sid string, port uint16) [][2]string {
	var out [][2]string
	for name, vs := range h {
		name = textproto.CanonicalMIMEHeaderKey(name)
		if hopByHop[name] || dropped[name] || name == "Cache-Control" || name == "Referrer-Policy" || name == "X-Content-Type-Options" {
			continue
		}
		for _, v := range vs {
			if strings.ContainsAny(v, "\r\n\x00") {
				continue
			}
			switch name {
			case "Location", "Content-Location":
				v = LocationFor(v, sid, port)
			case "Link":
				if v = link(v); v == "" {
					continue
				}
			}
			out = append(out, [2]string{name, v})
		}
	}
	out = append(out,
		[2]string{"Cache-Control", "no-store"},
		[2]string{"X-Content-Type-Options", "nosniff"},
		[2]string{"Referrer-Policy", "no-referrer"},
	)
	return out
}

// link drops a Link header that asks the browser to install a service
// worker or preload from another origin, the two things a header can do
// beyond the page; other links pass.
func link(v string) string {
	lv := strings.ToLower(v)
	if strings.Contains(lv, "serviceworker") {
		return ""
	}
	if strings.Contains(lv, "preload") || strings.Contains(lv, "prefetch") || strings.Contains(lv, "preconnect") {
		start, end := strings.IndexByte(v, '<'), strings.IndexByte(v, '>')
		if start < 0 || end < start {
			return ""
		}
		target := v[start+1 : end]
		if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
			return ""
		}
	}
	return v
}

// local are the names a container calls itself by.
var local = map[string]bool{"localhost": true, "127.0.0.1": true, "[::1]": true}

// LocationFor rewrites a redirect from the container on port that leads to
// it -- or to another published port of the same session, an application
// sending the browser to its identity provider -- to where the browser
// reaches it:
//
//	/x                         -> /s/<sid>/<port>/x
//	http://localhost:8080/x    -> /s/<sid>/8080/x
//	http://127.0.0.1:9000/x    -> /s/<sid>/9000/x
//	//localhost:8080/x         -> /s/<sid>/8080/x
//
// Anything else -- a relative path, another site -- is left alone.
func LocationFor(v, sid string, port uint16) string { return locationFor(v, sid, port) }

func locationFor(v, sid string, port uint16) string {
	if !ValidID(sid) {
		return v
	}
	if strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//") && !strings.HasPrefix(v, "/\\") {
		if port == 0 {
			return v
		}
		return Prefix(sid, port) + v
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" || u.User != nil {
		return v
	}
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return v
	}
	host, ps := u.Hostname(), u.Port()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if !local[strings.ToLower(host)] {
		return v
	}
	p := uint64(80)
	if ps != "" {
		var err error
		if p, err = strconv.ParseUint(ps, 10, 16); err != nil || p == 0 {
			return v
		}
	} else if u.Scheme == "https" {
		p = 443
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	out := Prefix(sid, uint16(p)) + path
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		out += "#" + u.EscapedFragment()
	}
	return out
}

// ValidStatus is a status a Response can carry (the Fetch standard's range
// for a constructed response).
func ValidStatus(code int) bool { return code >= 200 && code <= 599 }
