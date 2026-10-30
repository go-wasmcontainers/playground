// Package usercontent writes the site of a usercontent origin
// (<org>-usercontent.github.io): the shell that shows a container's
// published port inside a playground, on an origin -- a site, github.io
// being on the Public Suffix List -- of its own, so that a container's page
// can never read or script the playground.
//
// One implementation, several deployments: each <org>-usercontent Pages
// repository runs `site usercontent --allow <its playground's origin>` and
// publishes what it writes. The allowed origin is the only page the shell
// talks to.
package usercontent

import (
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

//go:embed assets
var assets embed.FS

// Files are what the site holds, as written.
var Files = []string{"index.html", "shell.html", "shell.js", "sw.js"}

var hostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// ErrOrigin is an allowed origin that is not one.
var ErrOrigin = errors.New("not an origin")

// CheckOrigin validates the playground origin a site will allow: https and
// a host name, no path, no port but the default -- or, with local, a
// loopback http origin with a port, for tests. The value is written into
// JavaScript and HTML, so nothing else passes.
func CheckOrigin(origin string, local bool) error {
	u, err := url.Parse(origin)
	if err != nil || u.Opaque != "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: %q", ErrOrigin, origin)
	}
	if u.String() != origin && u.String() != origin+"/" {
		return fmt.Errorf("%w: %q is not written canonically", ErrOrigin, origin)
	}
	host, port := u.Hostname(), u.Port()
	switch {
	case u.Scheme == "https" && port == "" && hostRE.MatchString(host):
		return nil
	case local && u.Scheme == "http" && port != "" && (host == "127.0.0.1" || host == "localhost"):
		if p, err := strconv.Atoi(port); err == nil && p > 0 && p < 65536 {
			return nil
		}
	}
	return fmt.Errorf("%w: %q (https://host, lower case, no path)", ErrOrigin, origin)
}

// Render writes the site for allow into dir (created if missing).
func Render(allow, dir string, local bool) error {
	if err := CheckOrigin(allow, local); err != nil {
		return err
	}
	allow = strings.TrimSuffix(allow, "/")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range Files {
		b, err := assets.ReadFile("assets/" + name)
		if err != nil {
			return err
		}
		out := strings.ReplaceAll(string(b), "{{ALLOW}}", allow)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(out), 0o644); err != nil {
			return err
		}
	}
	return nil
}
