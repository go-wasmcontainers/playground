package portrelay

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-wasmcontainers/engine/portmap"
)

// answer records what the relay sent.
type answer struct {
	mu     sync.Mutex
	status int
	header [][2]string
	body   strings.Builder
	ended  bool
	fail   int
	msg    string
}

func (a *answer) Head(s int, h [][2]string) { a.mu.Lock(); a.status, a.header = s, h; a.mu.Unlock() }
func (a *answer) Chunk(b []byte)            { a.mu.Lock(); a.body.Write(b); a.mu.Unlock() }
func (a *answer) End()                      { a.mu.Lock(); a.ended = true; a.mu.Unlock() }
func (a *answer) Fail(s int, m string)      { a.mu.Lock(); a.fail, a.msg = s, m; a.mu.Unlock() }

func (a *answer) field(name string) string {
	for _, kv := range a.header {
		if strings.EqualFold(kv[0], name) {
			return kv[1]
		}
	}
	return ""
}

const uc = "http://localhost:9999"

// container is an HTTP server published on port 8080 of a Virtual, as a
// container's `-p 8080:80` is in a browser.
func container(t *testing.T, h http.Handler) *Relay {
	t.Helper()
	v := portmap.NewVirtual()
	l, err := v.Listen(context.Background(), "tcp", netip.MustParseAddrPort("127.0.0.1:8080"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return &Relay{Transport: v.Transport(), Published: v.Ports}
}

func site() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("hello from " + r.Host + " via " + r.Header.Get("X-Forwarded-Prefix")))
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "s3cret", Path: "/", HttpOnly: true})
		w.Write([]byte("logged in"))
	})
	mux.HandleFunc("/whoami", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("cookie=" + r.Header.Get("Cookie")))
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:8080/hello", http.StatusFound)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	return mux
}

func get(r *Relay, sid, id, path string, header ...[2]string) *answer {
	a := &answer{}
	r.Serve(context.Background(), sid, uc, id, 8080, "GET", path, header, nil, a)
	return a
}

const id1, id2, id3 = "REQUESTrequestREQUEST01", "REQUESTrequestREQUEST02", "REQUESTrequestREQUEST03"

func TestRelay(t *testing.T) {
	r := container(t, site())
	sid, nonce, err := r.Open(8080, uc)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := r.Nonce(sid); !ok || got != nonce || sid == nonce {
		t.Fatalf("nonce %q %v", got, ok)
	}
	a := get(r, sid, id1, "/hello")
	if a.fail != 0 || a.status != 200 || !a.ended {
		t.Fatalf("GET /hello: %+v", a)
	}
	if want := "hello from localhost:8080 via /s/" + sid + "/8080"; a.body.String() != want {
		t.Fatalf("body %q, want %q", a.body.String(), want)
	}

	// The container's Set-Cookie stays in the jar; the next request carries
	// it, and whatever cookie the page sent is not passed.
	a = get(r, sid, id2, "/login")
	if a.field("Set-Cookie") != "" {
		t.Fatalf("Set-Cookie reached the browser: %v", a.header)
	}
	a = get(r, sid, id3, "/whoami", [2]string{"Cookie", "forged=1"})
	if a.body.String() != "cookie=session=s3cret" {
		t.Fatalf("the jar: %q", a.body.String())
	}

	// Another session has its own jar.
	other, _, err := r.Open(8080, uc)
	if err != nil {
		t.Fatal(err)
	}
	if a := get(r, other, id1, "/whoami"); a.body.String() != "cookie=" {
		t.Fatalf("a session saw another's cookie: %q", a.body.String())
	}

	// A redirect is the browser's to follow, rewritten to the route.
	a = get(r, sid, id1, "/redirect")
	if a.status != http.StatusFound || a.field("Location") != "/s/"+sid+"/8080/hello" {
		t.Fatalf("redirect: %d %v", a.status, a.header)
	}
}

func TestRelayRefuses(t *testing.T) {
	r := container(t, site())
	sid, _, err := r.Open(8080, uc)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, sid, uc, id string
		port              int
		method, path      string
		want              int
	}{
		{"a session it never issued", "SESSIONsessionSESSION99", uc, id1, 8080, "GET", "/hello", 503},
		{"another port", sid, uc, id1, 8081, "GET", "/hello", 403},
		{"another usercontent origin", sid, "http://localhost:1", id1, 8080, "GET", "/hello", 403},
		{"a bad request id", sid, uc, "x", 8080, "GET", "/hello", 400},
		{"a method not relayed", sid, uc, id1, 8080, "CONNECT", "/hello", 400},
		{"a path without /", sid, uc, id1, 8080, "GET", "hello", 400},
	} {
		a := &answer{}
		r.Serve(context.Background(), c.sid, c.uc, c.id, c.port, c.method, c.path, nil, nil, a)
		if a.fail != c.want || a.status != 0 {
			t.Errorf("%s: fail %d (%s), status %d; want fail %d", c.name, a.fail, a.msg, a.status, c.want)
		}
	}
}

func TestOpenRefuses(t *testing.T) {
	r := container(t, site())
	if _, _, err := r.Open(8081, uc); !errors.Is(err, ErrPort) {
		t.Errorf("an unpublished port: %v", err)
	}
	for _, o := range []string{"https://evil.example", "http://evil.example:8080", "http://localhost", "javascript:alert(1)"} {
		if _, _, err := r.Open(8080, o); err == nil {
			t.Errorf("usercontent %q accepted", o)
		}
	}
	if o, err := Origin(""); err != nil || o != DefaultUsercontent {
		t.Errorf("the default: %q %v", o, err)
	}
	r.MaxSessions = 2
	r.Open(8080, uc)
	r.Open(8080, uc)
	if _, _, err := r.Open(8080, uc); !errors.Is(err, ErrFull) {
		t.Errorf("a third session: %v", err)
	}
}

// Abort and Close stop a request the container never answers.
func TestAbortAndClose(t *testing.T) {
	r := container(t, site())
	sid, _, _ := r.Open(8080, uc)
	for _, stop := range []func(){func() { r.Abort(sid, id1) }, func() { r.Close(sid) }} {
		a := &answer{}
		done := make(chan struct{})
		go func() {
			r.Serve(context.Background(), sid, uc, id1, 8080, "GET", "/slow", nil, nil, a)
			close(done)
		}()
		time.Sleep(100 * time.Millisecond)
		stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the request did not stop")
		}
		if a.fail != http.StatusBadGateway {
			t.Fatalf("stopped request: %+v", a)
		}
	}
	if r.Sessions() != 0 {
		t.Fatalf("%d sessions after Close", r.Sessions())
	}
}

func TestMaxRequests(t *testing.T) {
	r := container(t, site())
	r.MaxRequests = 1
	sid, _, _ := r.Open(8080, uc)
	go r.Serve(context.Background(), sid, uc, id1, 8080, "GET", "/slow", nil, nil, &answer{})
	time.Sleep(100 * time.Millisecond)
	if a := get(r, sid, id2, "/hello"); a.fail != http.StatusServiceUnavailable {
		t.Fatalf("over the bound: %+v", a)
	}
	if a := get(r, sid, id1, "/hello"); a.fail != http.StatusConflict {
		t.Fatalf("a running id again: %+v", a)
	}
	r.Close(sid)
}
