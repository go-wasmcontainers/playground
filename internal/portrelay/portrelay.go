// Package portrelay is the engine worker's side of the HTTP route from a
// page to a container's published port (`wctr run -p` in a browser; the
// policy of what passes is internal/portroute).
//
// The playground opens a session for one published port: Open returns a
// session id, which names the port's pages on the usercontent origin
// (/s/<sid>/<port>/), and a nonce, which the view hands to the shell there.
// Each request the shell relays comes back here with that session id; a
// session the engine did not issue, or a port it was not opened for, is
// refused. A request goes to the container through portmap.Virtual's
// transport, with the session's cookies: a service worker can neither see
// nor set a cookie, so the jar is here, one per session, and a container's
// Set-Cookie never reaches the browser.
package portrelay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"sync"

	"github.com/go-wasmcontainers/playground/internal/portroute"
	"github.com/go-wasmcontainers/playground/internal/usercontent"
)

// DefaultUsercontent is the origin containers' pages are shown on.
const DefaultUsercontent = "https://go-wasmcontainers-usercontent.github.io"

// Bounds of the relay.
const (
	DefaultMaxSessions = 32
	DefaultMaxRequests = 256 // per session: the shell's own bound
	chunkSize          = 64 << 10
)

// Errors a caller can tell apart.
var (
	ErrSession = errors.New("no such session")
	ErrFull    = errors.New("too many sessions or requests")
	ErrPort    = errors.New("that port is not published")
)

// Origin is the usercontent origin a session may use: DefaultUsercontent,
// or a loopback http origin (a local build or test, where the usercontent
// site is served on another port). Nothing else: a link that named another
// origin would show the containers' pages to that site.
func Origin(o string) (string, error) {
	if o == "" || o == DefaultUsercontent {
		return DefaultUsercontent, nil
	}
	u, err := url.Parse(o)
	if err != nil || u.Scheme != "http" || usercontent.CheckOrigin(o, true) != nil {
		return "", fmt.Errorf("usercontent origin %q: only %s, or http://localhost:PORT or http://127.0.0.1:PORT", o, DefaultUsercontent)
	}
	return o, nil
}

// Answer receives a relayed request's answer: Head once, then Chunk any
// number of times, then End -- or Fail, before or after Head.
type Answer interface {
	Head(status int, header [][2]string)
	Chunk(b []byte)
	End()
	Fail(status int, msg string)
}

// Relay holds the sessions of one engine.
type Relay struct {
	// Transport reaches a container's port by the URL's port
	// ("http://localhost:8080/" is host port 8080): portmap.Virtual's.
	Transport http.RoundTripper
	// Published lists the ports listening; Open refuses any other.
	Published func() []uint16
	// MaxSessions and MaxRequests bound the relay (0: the defaults).
	MaxSessions, MaxRequests int

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	nonce, usercontent string
	port               uint16
	jar                *cookiejar.Jar
	client             *http.Client
	reqs               map[string]context.CancelFunc
}

// Open starts a session for the published port, shown on usercontent (see
// Origin), and returns its id and nonce.
func (r *Relay) Open(port uint16, usercontent string) (sid, nonce string, err error) {
	uc, err := Origin(usercontent)
	if err != nil {
		return "", "", err
	}
	published := false
	if r.Published != nil {
		for _, p := range r.Published() {
			published = published || p == port
		}
	}
	if !published {
		return "", "", fmt.Errorf("%w: %d", ErrPort, port)
	}
	jar, err := cookiejar.New(nil) // one host (localhost:<port>): no public suffix to decide
	if err != nil {
		return "", "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions == nil {
		r.sessions = map[string]*session{}
	}
	if len(r.sessions) >= or(r.MaxSessions, DefaultMaxSessions) {
		return "", "", fmt.Errorf("%w: %d sessions", ErrFull, len(r.sessions))
	}
	sid, nonce = randomID(), randomID()
	r.sessions[sid] = &session{
		nonce: nonce, usercontent: uc, port: port, jar: jar, reqs: map[string]context.CancelFunc{},
		client: &http.Client{
			Transport: r.Transport,
			Jar:       jar,
			// The browser follows a redirect itself, through the route
			// (portroute rewrites a Location to a container's port).
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	return sid, nonce, nil
}

// Close ends a session and every request still running in it.
func (r *Relay) Close(sid string) {
	r.mu.Lock()
	var cancels []context.CancelFunc
	if s := r.sessions[sid]; s != nil {
		for _, cancel := range s.reqs { // under the lock: a finishing Serve deletes from it
			cancels = append(cancels, cancel)
		}
	}
	delete(r.sessions, sid)
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// Abort stops a request the page gave up (the shell's abort).
func (r *Relay) Abort(sid, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sessions[sid]; s != nil {
		if cancel := s.reqs[id]; cancel != nil {
			cancel()
			delete(s.reqs, id)
		}
	}
}

// Serve relays one request of session sid's pages to its container, and
// sends the answer to a. It returns when the answer is complete.
func (r *Relay) Serve(ctx context.Context, sid, uc, id string, port int, method, path string, header [][2]string, body []byte, a Answer) {
	if !portroute.ValidID(id) {
		a.Fail(http.StatusBadRequest, "bad request id")
		return
	}
	r.mu.Lock()
	s := r.sessions[sid]
	switch {
	case s == nil:
		r.mu.Unlock()
		a.Fail(http.StatusServiceUnavailable, ErrSession.Error())
		return
	case uc != s.usercontent || port != int(s.port):
		r.mu.Unlock()
		a.Fail(http.StatusForbidden, "not this session's port")
		return
	case s.reqs[id] != nil:
		r.mu.Unlock()
		a.Fail(http.StatusConflict, "a request with that id is running")
		return
	case len(s.reqs) >= or(r.MaxRequests, DefaultMaxRequests):
		r.mu.Unlock()
		a.Fail(http.StatusServiceUnavailable, ErrFull.Error())
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	s.reqs[id] = cancel
	client := s.client
	r.mu.Unlock()
	defer func() {
		cancel()
		r.mu.Lock()
		delete(s.reqs, id)
		r.mu.Unlock()
	}()

	req, err := portroute.CheckRequest(port, method, path, header, body, sid, s.usercontent)
	if err != nil {
		a.Fail(http.StatusBadRequest, err.Error())
		return
	}
	hreq, err := http.NewRequestWithContext(ctx, req.Method, "http://localhost:"+strconv.Itoa(port)+req.Path, bytes.NewReader(req.Body))
	if err != nil {
		a.Fail(http.StatusBadRequest, err.Error())
		return
	}
	hreq.Header = req.Header
	res, err := client.Do(hreq)
	if err != nil {
		a.Fail(http.StatusBadGateway, "the container could not be reached")
		return
	}
	defer res.Body.Close()
	if !portroute.ValidStatus(res.StatusCode) {
		a.Fail(http.StatusBadGateway, "the container answered status "+strconv.Itoa(res.StatusCode))
		return
	}
	a.Head(res.StatusCode, portroute.Response(res.Header, sid, uint16(port)))
	buf := make([]byte, chunkSize)
	var total int64
	for {
		n, err := res.Body.Read(buf)
		if n > 0 {
			total += int64(n)
			if total > portroute.MaxResponse {
				a.Fail(http.StatusBadGateway, "the answer is too large")
				return
			}
			a.Chunk(append([]byte(nil), buf[:n]...))
		}
		if err == io.EOF {
			a.End()
			return
		}
		if err != nil {
			a.Fail(http.StatusBadGateway, "the container's answer broke off")
			return
		}
	}
}

// Sessions is how many sessions are open.
func (r *Relay) Sessions() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

// Nonce returns a session's nonce (the view gives it to the shell).
func (r *Relay) Nonce(sid string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[sid]
	if s == nil {
		return "", false
	}
	return s.nonce, true
}

func or(n, def int) int {
	if n > 0 {
		return n
	}
	return def
}

// randomID is 18 random bytes, URL-safe base64: 24 characters, a portroute
// ValidID.
func randomID() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		panic("portrelay: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
