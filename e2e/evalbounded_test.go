//go:build e2e

package e2e

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// evalWithin evaluates js on the page and returns an error, carrying a
// diagnosis, when the page does not answer within limit. A plain
// page.Evaluate waits for ever on a page whose main thread is stuck, and the
// test binary then lives until go test's own timeout kills it and every test
// after it never runs (TestPaneRunRmCycles hung 20 minutes in __frStop on one
// local run).
func evalWithin(page playwright.Page, js string, limit time.Duration) (any, error) {
	type res struct {
		v   any
		err error
	}
	ch := make(chan res, 1)
	go func() {
		v, err := page.Evaluate(js)
		ch <- res{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-time.After(limit):
		return nil, fmt.Errorf("page.Evaluate(%s) did not return in %v\n%s", js, limit, diagnoseMainThread(page))
	}
}

// evalBounded is evalWithin that fails the test.
func evalBounded(t *testing.T, page playwright.Page, js string, limit time.Duration) any {
	t.Helper()
	v, err := evalWithin(page, js, limit)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// diagnoseMainThread says what the browser's processes are doing, from the
// browser's own CDP endpoint (SystemInfo.getProcessInfo), which a stuck page
// cannot block: the CPU seconds each process used per second. A renderer that
// burns a full core is running script (a loop, a spin-wait); a renderer at
// zero is blocked waiting (a lock, a synchronous call into the GPU process,
// which is then the one to look at). The page's own debugger cannot answer for
// a thread that never returns to its event loop, so it is not asked.
func diagnoseMainThread(page playwright.Page) string {
	b := page.Context().Browser()
	if b == nil {
		return "no browser to ask"
	}
	cdp, err := b.NewBrowserCDPSession()
	if err != nil {
		return "no browser CDP session: " + err.Error()
	}
	sample := func() (map[string]float64, error) {
		type res struct {
			v   any
			err error
		}
		ch := make(chan res, 1)
		go func() { v, err := cdp.Send("SystemInfo.getProcessInfo", nil); ch <- res{v, err} }()
		select {
		case r := <-ch:
			if r.err != nil {
				return nil, r.err
			}
			m, _ := r.v.(map[string]any)
			list, _ := m["processInfo"].([]any)
			out := map[string]float64{}
			for _, e := range list {
				p, _ := e.(map[string]any)
				id, _ := p["id"].(float64)
				typ, _ := p["type"].(string)
				cpu, _ := p["cpuTime"].(float64)
				out[fmt.Sprintf("%s#%.0f", typ, id)] = cpu
			}
			return out, nil
		case <-time.After(10 * time.Second):
			return nil, fmt.Errorf("SystemInfo.getProcessInfo: no answer in 10 s")
		}
	}
	a, err := sample()
	if err != nil {
		return "the browser does not answer CDP: " + err.Error()
	}
	t0 := time.Now()
	time.Sleep(time.Second)
	z, err := sample()
	if err != nil {
		return "the browser does not answer CDP: " + err.Error()
	}
	dt := time.Since(t0).Seconds()
	names := make([]string, 0, len(z))
	for k := range z {
		names = append(names, k)
	}
	sort.Strings(names)
	var sb strings.Builder
	sb.WriteString("CPU seconds per second of each browser process:\n")
	for _, k := range names {
		if before, ok := a[k]; ok {
			fmt.Fprintf(&sb, "  %s %.2f\n", k, (z[k]-before)/dt)
		}
	}
	return sb.String()
}

// A page whose main thread never returns must make evalWithin FAIL within its
// limit, and say that its renderer is burning a core; a healthy page answers.
func TestEvalWithinBoundsAStuckPage(t *testing.T) {
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
	if v, err := evalWithin(page, `() => 6 * 7`, 20*time.Second); err != nil || v != 42 {
		t.Fatalf("a healthy page: %v, %v", v, err)
	}
	if _, err := page.Evaluate(`() => { setTimeout(() => { for (;;) {} }, 0); return true; }`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = evalWithin(page, `() => 1`, 3*time.Second)
	if err == nil {
		t.Fatal("a page stuck in a loop answered")
	}
	if d := time.Since(start); d > 60*time.Second {
		t.Fatalf("the bound took %v", d)
	}
	msg := err.Error()
	if !strings.Contains(msg, "did not return in 3s") {
		t.Fatalf("the error does not name the bound: %v", err)
	}
	busy := false
	for _, m := range regexp.MustCompile(`renderer#\d+ (\d+\.\d+)`).FindAllStringSubmatch(msg, -1) {
		if v, _ := strconv.ParseFloat(m[1], 64); v > 0.5 {
			busy = true
		}
	}
	if !busy {
		t.Fatalf("no renderer shown burning a core: %v", err)
	}
	t.Logf("diagnosis: %v", err)
}
