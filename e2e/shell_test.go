//go:build e2e

package e2e

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// TestShell drives the shell image (go-wasmcontainers/shell, mirrored
// pinned by digest; E2E_REMOTE=1 builds the site with it) in the browser
// executor: exec into a running container, a pipeline across two
// spawned programs, an exit status, and an interactive shell on a
// terminal, typed into the page.
func TestShell(t *testing.T) {
	if os.Getenv("E2E_REMOTE") != "1" || os.Getenv("E2E_URL") != "" {
		t.Skip("set E2E_REMOTE=1 to mirror the shell image (network)")
	}
	pw, err := playwright.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Stop()
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true), Timeout: playwright.Float(180000)})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer browser.Close()
	page, err := browser.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	b := tab{t, page, "chromium/shell"}
	if _, err := page.Goto(serve(t, true) + "?ui=terminal"); err != nil {
		t.Fatal(err)
	}
	b.ready()

	b.must("wctr run --rm shell sh -c 'echo a | cat; exit 3'", 3, "a")
	b.must("wctr run -d --name c shell sleep 600", 0)
	// The pipeline counts what ls alone lists (sh, coreutils.wasm and one
	// link per tool): two spawned programs joined by a pipe agree with one.
	listed := 0
	for _, l := range strings.Split(b.must("wctr exec c ls /bin", 0, "sh", "coreutils.wasm"), "\n") {
		if f := strings.TrimSpace(l); f != "" && !strings.Contains(f, "wctr exec") && !strings.HasPrefix(f, "$") {
			listed++
		}
	}
	out := b.must("wctr exec c sh -c 'ls /bin | wc -l'", 0)
	if listed < 40 || !strings.Contains(out, "\n"+strconv.Itoa(listed)) {
		t.Fatalf("ls /bin listed %d entries; ls /bin | wc -l:\n%s", listed, out)
	}
	b.must("wctr exec c sh -c 'exit 3'", 3)

	// Interactive shells: what is typed is the container's. Into the
	// running container (exec -it), then a container's own first process
	// (run --rm -it, engine v0.30.0).
	interactiveShell(t, b, "wctr exec -it c sh", "echo hi-$((40+2))", "hi-42")
	b.must("wctr rm -f c", 0)
	interactiveShell(t, b, "wctr run --rm -it shell", "echo hi", "hi")
	// --rm: the container is gone once the shell exited.
	if out := b.must("wctr ps -a", 0); strings.Contains(out, "shell") {
		t.Fatalf("run --rm -it left its container:\n%s", out)
	}
}

// interactiveShell types line into the terminal, waits for the shell,
// types cmd and waits for want on a line of its own (not the echo of cmd),
// then types exit 4: the line must end with status 4.
func interactiveShell(t *testing.T, b tab, line, cmd, want string) {
	t.Helper()
	page := b.page
	n := int(num(b.eval(`() => window.__playground.state.transcript.length`)))
	before := len(b.screen())
	if err := page.Keyboard().Type(line); err != nil {
		t.Fatal(err)
	}
	page.Keyboard().Press("Enter")
	time.Sleep(time.Second)
	page.Keyboard().Type(cmd)
	page.Keyboard().Press("Enter")
	b.until(line+": the shell did not answer",
		`() => /(^|\n)`+regexpQuote(want)+`[ \t]*(\n|$)/.test(window.__playground.text().slice(`+itoa(before)+`))`, time.Minute)
	page.Keyboard().Type("exit 4")
	page.Keyboard().Press("Enter")
	b.until(line+" did not end", `() => window.__playground.state.transcript.length > `+itoa(n), time.Minute)
	if code := int(num(b.eval(`() => window.__playground.state.transcript[` + itoa(n) + `].code`))); code != 4 {
		t.Fatalf("%s exited %d, want 4:\n%s", line, code, b.screen())
	}
}

// regexpQuote escapes s for a JavaScript regular expression literal.
func regexpQuote(s string) string {
	var out strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\^$.|?*+()[]{}/-`, r) {
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	return out.String()
}

func itoa(n int) string { return strconv.Itoa(n) }
