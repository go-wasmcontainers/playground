// Package class is the playground's "Join class": the engine worker joins
// a relay class with a join code the learner typed, puts a "class" network
// on the engine (network.Manager.AddDriver), keeps the session token in the
// engine's state store (IndexedDB) so a reload rejoins without the code,
// and leaves on request (the token is deleted, the network removed).
//
// The join code is a secret: it is used once, never stored, never logged.
// The relay is named by an https origin the learner typed; the page's
// service worker extends the engine worker's CSP connect-src to that origin
// only.
package class

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/go-wasmcontainers/engine/netdriver/relaynet"
	"github.com/go-wasmcontainers/engine/network"
	"github.com/go-wasmcontainers/engine/state"
)

// NetworkName is the network a joined class appears as (run --network class).
const NetworkName = "class"

const tokenKey = "relay/session"

// Origin validates a relay address the learner typed and returns its
// origin, "https://host[:port]": https only (the page is isolated and the
// token travels in the WebSocket handshake), no credentials, path, query
// or fragment.
func Origin(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) > 255 {
		return "", errors.New("relay address too long")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("relay address: %v", err)
	}
	switch {
	case u.Scheme != "https":
		return "", errors.New("the relay address must start with https://")
	case u.User != nil:
		return "", errors.New("the relay address must not carry a user or password")
	case u.Host == "" || u.Hostname() == "":
		return "", errors.New("the relay address has no host")
	case u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/"):
		return "", errors.New("the relay address is just https://host[:port]")
	}
	for _, c := range u.Host {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == ':' || c == '[' || c == ']') {
			return "", errors.New("the relay host has characters a host name cannot have")
		}
	}
	return "https://" + strings.ToLower(u.Host), nil
}

// Status is what the page shows.
type Status struct {
	State   string `json:"state"` // "joined", "left", "error"
	Relay   string `json:"relay,omitempty"`
	Label   string `json:"label,omitempty"`  // this browser's name in the class
	Domain  string `json:"domain,omitempty"` // the class's DNS domain
	Subnet  string `json:"subnet,omitempty"`
	Network string `json:"network,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Class joins, resumes and leaves.
type Class struct {
	Store state.Store
	Nets  *network.Manager
	HTTP  *http.Client

	mu     sync.Mutex
	joined *relaynet.Joined
	relay  string
}

type record struct {
	Relay string `json:"relay"`
	Token string `json:"token"`
}

func (c *Class) status(j *relaynet.Joined) Status {
	w := j.Session().Welcome()
	return Status{State: "joined", Relay: c.relay, Label: w.GetSession().GetLabel(), Domain: w.GetNetwork().GetDnsDomain(),
		Subnet: w.GetSession().GetSubnet(), Network: NetworkName}
}

func (c *Class) attach(relay string, j *relaynet.Joined) (Status, error) {
	if _, err := c.Nets.AddDriver(NetworkName, "relay", j); err != nil {
		j.Close()
		return Status{}, err
	}
	c.joined, c.relay = j, relay
	b, _ := json.Marshal(record{Relay: relay, Token: j.Token()})
	if err := c.Store.Put(tokenKey, b); err != nil {
		return Status{}, err
	}
	return c.status(j), nil
}

// Join trades the code for a session and adds the class network.
func (c *Class) Join(ctx context.Context, relay, code, name string) (Status, error) {
	origin, err := Origin(relay)
	if err != nil {
		return Status{}, err
	}
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 64 {
		return Status{}, errors.New("enter the join code the teacher gave")
	}
	if name = strings.TrimSpace(name); name == "" || len(name) > 64 {
		return Status{}, errors.New("enter your name (64 characters at most)")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.joined != nil {
		return Status{}, errors.New("already in a class: leave it first")
	}
	j, err := relaynet.Join(ctx, origin, code, name, c.HTTP)
	if err != nil {
		// The relay's message, never the code.
		return Status{}, fmt.Errorf("could not join the class at %s: %v", origin, redact(err, code))
	}
	return c.attach(origin, j)
}

// redact removes a secret from an error's text, in case a layer echoed it.
func redact(err error, secret string) string {
	s := err.Error()
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "[join code]")
	}
	return s
}

// Resume rejoins with the token kept from the last visit, if it was for
// relay. It reports whether a class is joined.
func (c *Class) Resume(ctx context.Context, relay string) (Status, bool, error) {
	origin, err := Origin(relay)
	if err != nil {
		return Status{}, false, err
	}
	b, err := c.Store.Get(tokenKey)
	if err != nil {
		return Status{}, false, nil // none kept
	}
	var r record
	if json.Unmarshal(b, &r) != nil || r.Relay != origin || r.Token == "" || len(r.Token) > 4096 {
		c.Store.Delete(tokenKey) // another relay's, or damaged
		return Status{}, false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.joined != nil {
		return c.status(c.joined), true, nil
	}
	j, err := relaynet.Connect(ctx, origin, r.Token, c.HTTP)
	if err != nil {
		return Status{}, false, fmt.Errorf("the class session at %s no longer works (join again): %v", origin, err)
	}
	st, err := c.attach(origin, j)
	return st, err == nil, err
}

// Leave removes the class network, ends the session and forgets the token.
func (c *Class) Leave() (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.Store.Delete(tokenKey)
	if c.joined != nil {
		if drv, rerr := c.Nets.RemoveDriver(NetworkName); rerr == nil {
			drv.Close()
		} else {
			c.joined.Close()
		}
		c.joined = nil
	}
	return Status{State: "left", Relay: c.relay}, err
}
