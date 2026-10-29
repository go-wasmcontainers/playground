package portroute

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

const sid = "AAAAAAAAAAAAAAAAAAAAAA" // 22 characters
const uc = "https://go-wasmcontainers-usercontent.github.io"

func TestCheckRequest(t *testing.T) {
	r, err := CheckRequest(8080, "POST", "/login?next=/", [][2]string{
		{"content-type", "application/x-www-form-urlencoded"},
		{"Authorization", "Bearer x"},
		{"Cookie", "stolen=1"},
		{"Host", "evil"},
		{"Connection", "close"},
		{"X-Forwarded-Host", "evil"},
		{"Forwarded", "for=1.2.3.4"},
	}, []byte("a=b"), sid, uc)
	if err != nil {
		t.Fatal(err)
	}
	if r.Port != 8080 || r.Method != "POST" || r.Path != "/login?next=/" || string(r.Body) != "a=b" {
		t.Fatalf("%+v", r)
	}
	h := r.Header
	if h.Get("Authorization") != "Bearer x" || h.Get("Content-Type") == "" {
		t.Fatalf("an API's own fields were dropped: %v", h)
	}
	for _, gone := range []string{"Cookie", "Host", "Connection", "Forwarded"} {
		if h.Get(gone) != "" {
			t.Errorf("%s relayed: %v", gone, h)
		}
	}
	if h.Get("X-Forwarded-Host") != "go-wasmcontainers-usercontent.github.io" || h.Get("X-Forwarded-Proto") != "https" ||
		h.Get("X-Forwarded-Prefix") != "/s/"+sid+"/8080" {
		t.Fatalf("forwarded fields: %v", h)
	}
}

func TestCheckRequestRefuses(t *testing.T) {
	many := make([][2]string, MaxHeaders+1)
	for i := range many {
		many[i] = [2]string{"X-A", "b"}
	}
	big := [][2]string{{"X-A", strings.Repeat("a", MaxHeaderBytes)}}
	for name, tc := range map[string]struct {
		port   int
		method string
		path   string
		h      [][2]string
		body   []byte
		sid    string
	}{
		"port 0":         {0, "GET", "/", nil, nil, sid},
		"port 65536":     {65536, "GET", "/", nil, nil, sid},
		"CONNECT":        {80, "CONNECT", "/", nil, nil, sid},
		"TRACE":          {80, "TRACE", "/", nil, nil, sid},
		"relative path":  {80, "GET", "x", nil, nil, sid},
		"CRLF in path":   {80, "GET", "/\r\nX: y", nil, nil, sid},
		"long path":      {80, "GET", "/" + strings.Repeat("a", MaxPath), nil, nil, sid},
		"many fields":    {80, "GET", "/", many, nil, sid},
		"large fields":   {80, "GET", "/", big, nil, sid},
		"bad field name": {80, "GET", "/", [][2]string{{"X A", "b"}}, nil, sid},
		"CRLF in value":  {80, "GET", "/", [][2]string{{"X-A", "b\r\nX-B: c"}}, nil, sid},
		"large body":     {80, "POST", "/", nil, make([]byte, MaxBody+1), sid},
		"bad session":    {80, "GET", "/", nil, nil, "short"},
		"empty name":     {80, "GET", "/", [][2]string{{"", "b"}}, nil, sid},
	} {
		if _, err := CheckRequest(tc.port, tc.method, tc.path, tc.h, tc.body, tc.sid, uc); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := CheckRequest(80, "GET", "/", nil, nil, sid, "::"); !errors.Is(err, ErrRefused) {
		t.Errorf("bad usercontent origin: %v", err)
	}
}

func TestResponse(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "text/html")
	h.Add("Set-Cookie", "s=1")
	h.Set("Clear-Site-Data", `"*"`)
	h.Set("Service-Worker-Allowed", "/")
	h.Set("Cache-Control", "public, max-age=99999")
	h.Set("Connection", "keep-alive")
	h.Set("Content-Length", "12")
	h.Set("Permissions-Policy", "camera=*")
	h.Set("Location", "http://localhost:9000/authorize?x=1")
	h.Set("X-Evil", "a\r\nb")
	h.Add("Link", "</sw.js>; rel=serviceworker")
	h.Add("Link", "<https://other.example/x.js>; rel=preload")
	h.Add("Link", "</style.css>; rel=preload")
	h.Add("Link", "</next>; rel=next")
	got := map[string][]string{}
	for _, kv := range Response(h, sid, 8080) {
		got[kv[0]] = append(got[kv[0]], kv[1])
	}
	for _, gone := range []string{"Set-Cookie", "Clear-Site-Data", "Service-Worker-Allowed", "Connection", "Content-Length", "Permissions-Policy", "X-Evil"} {
		if len(got[gone]) > 0 {
			t.Errorf("%s reached the browser: %v", gone, got[gone])
		}
	}
	if strings.Join(got["Cache-Control"], ",") != "no-store" || got["X-Content-Type-Options"][0] != "nosniff" || got["Referrer-Policy"][0] != "no-referrer" {
		t.Errorf("the route's own fields: %v", got)
	}
	if got["Location"][0] != "/s/"+sid+"/9000/authorize?x=1" {
		t.Errorf("Location %v", got["Location"])
	}
	if l := strings.Join(got["Link"], "|"); l != "</style.css>; rel=preload|</next>; rel=next" {
		t.Errorf("Link %q", l)
	}
	if got["Content-Type"][0] != "text/html" {
		t.Errorf("Content-Type %v", got["Content-Type"])
	}
}

func TestLocationFor(t *testing.T) {
	p := "/s/" + sid
	for in, want := range map[string]string{
		"/x":                             p + "/8080/x",
		"/":                              p + "/8080/",
		"http://localhost:8080/x?a=b#f":  p + "/8080/x?a=b#f",
		"http://127.0.0.1:9000":          p + "/9000/",
		"//localhost:9000/cb":            p + "/9000/cb",
		"http://localhost/x":             p + "/80/x",
		"https://localhost/x":            p + "/443/x",
		"http://[::1]:7000/x":            p + "/7000/x",
		"x":                              "x",
		"../x":                           "../x",
		"https://idp.example.org/auth":   "https://idp.example.org/auth",
		"//evil.example/x":               "//evil.example/x",
		"/\\evil.example":                "/\\evil.example",
		"http://user@localhost:8080/":    "http://user@localhost:8080/",
		"javascript://localhost:80/%0a1": "javascript://localhost:80/%0a1",
		"http://localhost:0/x":           "http://localhost:0/x",
		"http://localhost:99999/x":       "http://localhost:99999/x",
	} {
		if got := LocationFor(in, sid, 8080); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if got := LocationFor("/x", "bad", 8080); got != "/x" {
		t.Errorf("a bad session rewrote: %q", got)
	}
	if got := LocationFor("/x", sid, 0); got != "/x" {
		t.Errorf("port 0 rewrote: %q", got)
	}
	if !ValidStatus(200) || !ValidStatus(599) || ValidStatus(101) || ValidStatus(600) {
		t.Error("ValidStatus")
	}
}

// FuzzRoute: whatever a container answers and whatever a page relays, the
// policy never panics, never lets a CR or LF through, never lets a dropped
// field through, and a rewritten Location stays on the usercontent origin.
func FuzzRoute(f *testing.F) {
	f.Add("/x", "Location", "http://localhost:8080/y", "GET", "/p", "X-A", "b")
	f.Add("//evil", "Link", "</sw.js>; rel=serviceworker", "POST", "/", "Cookie", "c")
	f.Fuzz(func(t *testing.T, loc, name, value, method, path, rname, rvalue string) {
		got := LocationFor(loc, sid, 8080)
		if got != loc && !strings.HasPrefix(got, "/s/"+sid+"/") {
			t.Fatalf("%q rewritten off the origin: %q", loc, got)
		}
		h := http.Header{}
		h[name] = []string{value}
		h["Location"] = []string{loc}
		for _, kv := range Response(h, sid, 8080) {
			if strings.ContainsAny(kv[1], "\r\n\x00") {
				t.Fatalf("a control character reached the browser: %q", kv)
			}
			if dropped[kv[0]] || hopByHop[kv[0]] {
				t.Fatalf("%s reached the browser", kv[0])
			}
		}
		r, err := CheckRequest(8080, method, path, [][2]string{{rname, rvalue}}, nil, sid, uc)
		if err == nil {
			for k, vs := range r.Header {
				if hopByHop[k] || k == "Cookie" || k == "Host" {
					t.Fatalf("%s relayed", k)
				}
				for _, v := range vs {
					if strings.ContainsAny(v, "\r\n\x00") {
						t.Fatalf("a control character relayed: %q", v)
					}
				}
			}
			if strings.ContainsAny(r.Path, "\r\n\x00") || r.Path[0] != '/' {
				t.Fatalf("path %q", r.Path)
			}
		}
	})
}
