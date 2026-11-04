//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/mxschmitt/playwright-go"

	"github.com/go-wasmcontainers/playground/internal/webdriver"
)

// pager is what tab needs of a page: a playwright.Page, or the real Safari.
type pager interface {
	Evaluate(expression string, arg ...any) (any, error)
	Keyboard() playwright.Keyboard
}

// safariPage drives the real Safari through safaridriver (W3C WebDriver;
// Playwright's WebKit is not Safari).
type safariPage struct {
	s   *webdriver.Session
	ctx context.Context
}

// Evaluate runs js, an arrow function's source as tab uses them, and waits
// for a returned promise.
func (p safariPage) Evaluate(js string, _ ...any) (any, error) {
	raw, err := p.s.ExecuteAsync(p.ctx, `const f = (`+js+`);
const cb = arguments[arguments.length - 1];
Promise.resolve().then(() => typeof f === "function" ? f() : f)
  .then((v) => cb({ok: v === undefined ? null : v}), (e) => cb({err: String(e)}));`)
	if err != nil {
		return nil, err
	}
	var r struct {
		OK  any    `json:"ok"`
		Err string `json:"err"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if r.Err != "" {
		return nil, errors.New(r.Err)
	}
	return r.OK, nil
}

func (p safariPage) Keyboard() playwright.Keyboard { return safariKeys{p} }

type safariKeys struct{ p safariPage }

var specialKeys = map[string]string{"Enter": "", "Backspace": "", "Tab": "", "Escape": "",
	"ArrowLeft": "", "ArrowUp": "", "ArrowRight": "", "ArrowDown": ""}

func (k safariKeys) Type(text string, _ ...playwright.KeyboardTypeOptions) error {
	var keys []string
	for _, r := range text {
		keys = append(keys, string(r))
	}
	return k.p.s.SendKeys(k.p.ctx, keys)
}

func (k safariKeys) Press(key string, _ ...playwright.KeyboardPressOptions) error {
	if v, ok := specialKeys[key]; ok {
		key = v
	} else if len([]rune(key)) != 1 {
		return fmt.Errorf("safari keyboard: key %q is not supported", key)
	}
	return k.p.s.SendKeys(k.p.ctx, []string{key})
}

func (k safariKeys) Down(string) error { return errors.New("safari keyboard: Down is not supported") }
func (k safariKeys) Up(string) error   { return errors.New("safari keyboard: Up is not supported") }
func (k safariKeys) InsertText(string) error {
	return errors.New("safari keyboard: InsertText is not supported")
}

// TestModelDownloadSafari is TestModelDownload (the model stand-in server:
// download with progress, sha256 check, OPFS cache, reload, removal, corrupt
// and offline cases; with LLM_LIVE_HF=1 the real llm image too) on the real
// Safari. It runs only when asked, E2E_BROWSERS=safari, and then it FAILS,
// never skips, when Safari refuses the session (run `sudo safaridriver
// --enable` once), offers no WebGPU adapter, or only a fallback one.
func TestModelDownloadSafari(t *testing.T) {
	if !strings.Contains(os.Getenv("E2E_BROWSERS"), "safari") {
		t.Skip("local only: set E2E_BROWSERS=safari (needs `sudo safaridriver --enable` once)")
	}
	driver := os.Getenv("SAFARIDRIVER")
	if driver == "" {
		driver = "/usr/bin/safaridriver"
	}
	ctx := context.Background()
	c, stop, err := webdriver.Start(ctx, driver)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	s, err := c.NewSession(ctx, map[string]any{"browserName": "Safari"})
	if err != nil {
		var we *webdriver.Error
		if errors.As(err, &we) && strings.Contains(strings.ToLower(we.Message), "remote automation") {
			t.Fatalf("Safari refused the session: run `sudo safaridriver --enable` once (and tick Develop > Allow Remote Automation): %v", err)
		}
		t.Fatalf("Safari session: %v", err)
	}
	defer s.Delete(ctx)
	t.Logf("Safari %s", s.Version)

	st := newStub()
	sum := sha256.Sum256(st.data)
	spec := fmt.Sprintf("stubm,%d,%s,/stub/model.bin", len(st.data), hex.EncodeToString(sum[:]))
	base := serveWithStub(t, st)
	page := safariPage{s, ctx}
	b := tab{t, page, "safari " + s.Version}

	// The adapter first: no adapter, or a software one, fails the lane.
	if err := s.Navigate(ctx, base+"?ui=terminal&modelstub="+url.QueryEscape(spec)); err != nil {
		t.Fatal(err)
	}
	v, err := page.Evaluate(`async () => {
	  if (!navigator.gpu) return "no navigator.gpu";
	  const a = await navigator.gpu.requestAdapter();
	  if (!a) return "requestAdapter returned null";
	  if (a.isFallbackAdapter) return "fallback adapter";
	  return "ok " + a.info.vendor + " " + a.info.architecture + " f16=" + a.features.has("shader-f16") + " subgroups=" + a.features.has("subgroups");
	}`)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("adapter: %v", v)
	if str, _ := v.(string); !strings.HasPrefix(str, "ok ") {
		t.Fatalf("Safari has no usable GPU adapter: %v", v)
	}

	modelSteps(t, b, st, func(u string) error { return s.Navigate(ctx, u) },
		func() error { return s.Refresh(ctx) }, base+"?ui=terminal&modelstub="+url.QueryEscape(spec))
}
