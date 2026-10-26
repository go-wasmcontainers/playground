package class

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-wasmcontainers/relay/auth"
	v1 "github.com/go-wasmcontainers/relay/gen/relayv1"
	"github.com/go-wasmcontainers/relay/relay"
	"github.com/go-wasmcontainers/relay/server"

	"github.com/go-wasmcontainers/engine/network"
	"github.com/go-wasmcontainers/engine/state"
)

func TestOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://relay.example":        "https://relay.example",
		" https://Relay.Example:8443/": "https://relay.example:8443",
		"https://[::1]:8443":           "https://[::1]:8443",
	} {
		if got, err := Origin(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"http://relay.example", "wss://relay.example", "relay.example", "https://u:p@relay.example",
		"https://relay.example/x", "https://relay.example?a", "https://relay.example#f", "https://", "javascript:alert(1)",
		"https://relay.example\"; script-src *", "https://re lay.example", "https://" + strings.Repeat("a", 300)} {
		if got, err := Origin(bad); err == nil {
			t.Errorf("%q accepted as %q", bad, got)
		}
	}
}

func TestJoinResumeLeave(t *testing.T) {
	k, _ := auth.NewKeys()
	r, err := relay.New(relay.Config{Keys: k})
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := auth.RandomToken()
	s, err := server.New(server.Config{Relay: r, AdminToken: tok})
	if err != nil {
		t.Fatal(err)
	}
	pub := httptest.NewTLSServer(s.Public())
	defer pub.Close()
	cl, err := r.CreateClass(relay.ClassSpec{Name: "net101", Template: v1.Template_TEMPLATE_NEUTRON_DEFAULT})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := &state.Mem{}
	nets, _, _ := network.New(network.Options{})
	defer nets.Close()
	c := &Class{Store: store, Nets: nets, HTTP: pub.Client()}

	// A wrong code: refused, and the code is not in the message.
	if _, err := c.Join(ctx, pub.URL, "ZZZZZ-ZZZZZ", "alice"); err == nil || strings.Contains(err.Error(), "ZZZZZ-ZZZZZ") {
		t.Fatalf("wrong code: %v", err)
	}
	st, err := c.Join(ctx, pub.URL, cl.JoinCode, "alice")
	if err != nil || st.State != "joined" || st.Label == "" || st.Network != NetworkName {
		t.Fatal(st, err)
	}
	if _, err := nets.Get(NetworkName); err != nil {
		t.Fatal("no class network:", err)
	}
	if _, err := c.Join(ctx, pub.URL, cl.JoinCode, "alice"); err == nil {
		t.Fatal("joined twice")
	}
	ep, err := nets.Attach(ctx, NetworkName, "web")
	if err != nil || !ep.Addr().IsValid() {
		t.Fatal(ep, err)
	}
	b, err := store.Get(tokenKey)
	if err != nil || strings.Contains(string(b), cl.JoinCode) {
		t.Fatalf("kept record: %s %v", b, err)
	}

	// A reload: a new engine resumes with the kept token, without the code.
	nets2, _, _ := network.New(network.Options{})
	defer nets2.Close()
	c2 := &Class{Store: store, Nets: nets2, HTTP: pub.Client()}
	if _, ok, _ := c2.Resume(ctx, "https://other.example"); ok {
		t.Fatal("resumed for another relay")
	}
	if _, err := store.Get(tokenKey); err == nil {
		t.Fatal("another relay's resume did not drop the token")
	}
	store.Put(tokenKey, b)
	c.Leave() // the first engine's session ends (a reload kills it anyway)
	store.Put(tokenKey, b)
	st, ok, err := c2.Resume(ctx, pub.URL)
	if err != nil || !ok || st.Label == "" {
		t.Fatal(st, ok, err)
	}
	// Leave: no network, no token.
	if st, err := c2.Leave(); err != nil || st.State != "left" {
		t.Fatal(st, err)
	}
	if _, err := nets2.Get(NetworkName); err == nil {
		t.Fatal("class network still there")
	}
	if _, err := store.Get(tokenKey); err == nil {
		t.Fatal("token kept after leaving")
	}
	if _, ok, _ := c2.Resume(ctx, pub.URL); ok {
		t.Fatal("resumed after leaving")
	}
	// Refusals before any network call.
	for _, bad := range []struct{ relay, code, name string }{
		{"http://x", cl.JoinCode, "a"}, {pub.URL, "", "a"}, {pub.URL, cl.JoinCode, ""},
	} {
		if _, err := c2.Join(ctx, bad.relay, bad.code, bad.name); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
