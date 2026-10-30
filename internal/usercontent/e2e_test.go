//go:build e2e

package usercontent

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// The shell, in Chromium, with three local origins: the playground
// (http://127.0.0.1:A, where a stand-in for the view tab answers requests
// itself), the usercontent origin (http://localhost:B, serving the rendered
// site with no headers and 404 elsewhere, as GitHub Pages does), and a
// stranger (http://127.0.0.1:C).
//
//	go test -tags e2e ./internal/usercontent/

const sid = "SESSIONsessionSESSION12"
const nonce = "NONCEnonceNONCEnonce123"

// view is the stand-in for the playground's view tab: it embeds the shell,
// opens port 8080 when the shell is ready, and answers each request with a
// small site, in two chunks, with a Set-Cookie the route must drop.
const view = `<!doctype html><script>
window.log = {ready: false, reqs: [], content: null};
const UC = %q, NONCE = %q, SID = %q;
let shell;
window.addEventListener("message", (e) => {
  if (e.origin !== UC) return;
  const m = e.data;
  if (m.content) { window.log.content = m.content; return; }
  if (e.source !== shell.contentWindow) return;
  if (m.t === "ready") {
    window.log.ready = true;
    shell.contentWindow.postMessage({t: "open", nonce: window.openNonce || NONCE, port: 8080, path: "/index.html"}, UC);
    return;
  }
  if (m.t !== "req") return;
  window.log.reqs.push(m.method + " " + m.path);
  const files = {
    "/index.html": ["text/html", '<!doctype html><link rel=stylesheet href="style.css"><script src="app.js"></scr' + 'ipt><img id=i src="pix.svg"><p id=p>x</p><script>' +
      '(async () => { const get = async (u) => { try { const r = await fetch(u); return r.status + ":" + await r.text(); } catch (e) { return "throws " + e; } };' +
      ' const f = await get("api"); const other = await get("/s/OTHERsessionOTHERsession1/8080/x");' +
      ' await new Promise(r => setTimeout(r, 300));' +
      ' top.postMessage({content: {css: getComputedStyle(document.getElementById("p")).color, script: !!window.scriptRan,' +
      ' img: document.getElementById("i").naturalWidth, fetch: f, other: other.slice(0, 3), cookie: document.cookie}}, "*"); })();</scr' + 'ipt>'],
    "/style.css": ["text/css", "#p{color: rgb(1, 2, 3)}"],
    "/app.js": ["text/javascript", "window.scriptRan = true;"],
    "/pix.svg": ["image/svg+xml", '<svg xmlns="http://www.w3.org/2000/svg" width="7" height="7"/>'],
    "/api": ["text/plain", "from-the-engine"],
  };
  const f = files[m.path.split("?")[0]];
  const w = shell.contentWindow;
  if (!f) { w.postMessage({t: "head", nonce: NONCE, id: m.id, status: 404, headers: [["Content-Type", "text/plain"]]}, UC); w.postMessage({t: "end", nonce: NONCE, id: m.id}, UC); return; }
  w.postMessage({t: "head", nonce: NONCE, id: m.id, status: 200, headers: [["Content-Type", f[0]], ["Set-Cookie", "s=1"]]}, UC);
  const b = new TextEncoder().encode(f[1]);
  const half = Math.floor(b.length / 2);
  for (const part of [b.slice(0, half), b.slice(half)]) {
    const ab = part.buffer;
    w.postMessage({t: "chunk", nonce: NONCE, id: m.id, data: ab}, UC, [ab]);
  }
  w.postMessage({t: "end", nonce: NONCE, id: m.id}, UC);
});
window.addEventListener("load", () => {
  shell = document.createElement("iframe");
  shell.src = UC + "/shell.html?s=" + SID + "#" + NONCE;
  shell.style.cssText = "width:600px;height:400px";
  document.body.appendChild(shell);
});
</script><body></body>`

func listen(t *testing.T, h http.Handler) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
}

func TestShellInChromium(t *testing.T) {
	ucMux := http.NewServeMux()
	ucPort := listen(t, ucMux)
	uc := "http://localhost:" + ucPort

	views := http.NewServeMux()
	playPort := listen(t, views)
	play := "http://127.0.0.1:" + playPort
	views.HandleFunc("/view", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		page := fmt.Sprintf(view, uc, nonce, sid)
		if r.URL.Query().Get("bad") == "1" {
			page = strings.Replace(page, "<script>", "<script>window.openNonce = 'WRONGnonceWRONGnonce123';", 1)
		}
		fmt.Fprint(w, page)
	})
	strangers := http.NewServeMux()
	stranger := "http://127.0.0.1:" + listen(t, strangers)
	strangers.HandleFunc("/view", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, fmt.Sprintf(view, uc, nonce, sid))
	})

	dir := t.TempDir()
	if err := Render(play, dir, true); err != nil {
		t.Fatal(err)
	}
	files := http.FileServer(http.Dir(dir))
	ucMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/s/") {
			http.NotFound(w, r) // only the service worker serves these, as on Pages
			return
		}
		files.ServeHTTP(w, r)
	})

	pw, err := playwright.Run(&playwright.RunOptions{SkipInstallBrowsers: true})
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Stop()
	b, err := pw.Chromium.Launch()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	open := func(url string) playwright.Page {
		ctx, err := b.NewContext()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ctx.Close() })
		p, _ := ctx.NewPage()
		if _, err := p.Goto(url); err != nil {
			t.Fatal(err)
		}
		return p
	}
	wait := func(p playwright.Page, expr string, d time.Duration) any {
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			v, _ := p.Evaluate(expr)
			if v != nil && v != false {
				return v
			}
			time.Sleep(100 * time.Millisecond)
		}
		return nil
	}

	// The whole route: the content's own subresources and fetch go through
	// the usercontent worker, the body arrives in chunks, Set-Cookie never
	// reaches the browser, and a session with no shell gets a clean 503.
	p := open(play + "/view")
	got := wait(p, "window.log.content", 20*time.Second)
	if got == nil {
		l, _ := p.Evaluate("window.log")
		t.Fatalf("no content: %v", l)
	}
	c := got.(map[string]any)
	if c["css"] != "rgb(1, 2, 3)" || c["script"] != true || fmt.Sprint(c["img"]) != "7" || c["fetch"] != "200:from-the-engine" {
		t.Fatalf("content: %v", c)
	}
	if c["other"] != "503" {
		t.Fatalf("another session's path: %v", c)
	}
	if c["cookie"] != "" {
		t.Fatalf("a container set a cookie on the usercontent origin: %q", c["cookie"])
	}

	// A wrong nonce: the shell opens nothing.
	p = open(play + "/view?bad=1")
	if wait(p, "window.log.ready", 15*time.Second) == nil {
		t.Fatal("the shell never said ready")
	}
	time.Sleep(2 * time.Second)
	if n, _ := p.Evaluate("window.log.reqs.length"); fmt.Sprint(n) != "0" {
		t.Fatalf("a wrong nonce opened the port: %v", n)
	}

	// Another origin embedding the shell: refused, nothing is said to it.
	p = open(stranger + "/view")
	time.Sleep(4 * time.Second)
	if l, _ := p.Evaluate("window.log"); l.(map[string]any)["ready"] != false {
		t.Fatalf("the shell talked to %s: %v", stranger, l)
	}

	// Top-level, not embedded: refused.
	p = open(uc + "/shell.html?s=" + sid + "#" + nonce)
	if txt, _ := p.TextContent("body"); !strings.Contains(txt, "Refused") {
		t.Fatalf("top-level shell: %q", txt)
	}
	_ = os.Stderr
}
