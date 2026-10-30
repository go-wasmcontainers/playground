package usercontent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckOrigin(t *testing.T) {
	for _, ok := range []string{"https://go-wasmcontainers.github.io", "https://go-fileshare.github.io", "https://a.b"} {
		if err := CheckOrigin(ok, false); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "go-fileshare.github.io", "http://go-fileshare.github.io", "https://Go-Fileshare.github.io",
		"https://go-fileshare.github.io/playground", "https://go-fileshare.github.io:8443", "https://x@go-fileshare.github.io",
		"https://go-fileshare.github.io?x", "https://go-fileshare.github.io#x", `https://a.b"onload="x`, "https://localhost",
		"https://a.b/'", "javascript:alert(1)", "http://127.0.0.1:8080", "https://-a.b",
	} {
		if err := CheckOrigin(bad, false); !errors.Is(err, ErrOrigin) {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"http://127.0.0.1:8080", "http://localhost:1"} {
		if err := CheckOrigin(ok, true); err != nil {
			t.Errorf("local %s: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://127.0.0.1", "http://10.0.0.1:80", "http://127.0.0.1:0", "http://127.0.0.1:70000"} {
		if err := CheckOrigin(bad, true); err == nil {
			t.Errorf("local %q accepted", bad)
		}
	}
}

func TestRender(t *testing.T) {
	dir := t.TempDir()
	if err := Render("https://go-fileshare.github.io", dir, false); err != nil {
		t.Fatal(err)
	}
	for _, f := range Files {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "{{") {
			t.Errorf("%s: a placeholder is left", f)
		}
	}
	for _, f := range []string{"shell.js", "sw.js"} {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		if !strings.Contains(string(b), `const ALLOW = "https://go-fileshare.github.io";`) {
			t.Errorf("%s does not pin the origin", f)
		}
	}
	if err := Render("https://evil.example/x", dir, false); err == nil {
		t.Fatal("a bad origin rendered")
	}
	if err := Render("https://a.b", filepath.Join(dir, "index.html", "x"), false); err == nil {
		t.Fatal("an unwritable dir rendered")
	}
}
