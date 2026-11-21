//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"github.com/mxschmitt/playwright-go"
)

// demuxLogs joins the payloads of a Docker multiplexed stream (the engine's
// /containers/{id}/logs: an 8-byte header per frame, the stream in byte 0 and the
// payload length big-endian in bytes 4-7). A truncated last frame keeps what it
// holds; the second result counts the frames read.
func demuxLogs(b []byte) (string, int) {
	var sb strings.Builder
	frames := 0
	for len(b) >= 8 {
		n := int(binary.BigEndian.Uint32(b[4:8]))
		b = b[8:]
		if n > len(b) {
			n = len(b)
		}
		sb.Write(b[:n])
		b = b[n:]
		frames++
	}
	return sb.String(), frames
}

// containerLogs is the whole log of a container, as `wctr logs` reads it but not
// as the terminal shows it: the terminal is a screen, so a long log scrolls off
// it and its wrapped lines are cut. It asks the engine's logs endpoint over the
// page's bridge (the dashboard's http.req / http.head / http.data / http.end
// messages on the engine worker, with an id no pane request uses).
func containerLogs(t *testing.T, page playwright.Page, name string) string {
	t.Helper()
	v, err := page.Evaluate(`(name) => new Promise((resolve, reject) => {
  const w = window.__wctrEngine, id = 1073741000;
  const parts = [];
  let status = 0;
  const timer = setTimeout(() => { w.removeEventListener("message", on); reject(new Error("the engine did not answer the logs request")); }, 30000);
  function on(ev) {
    const m = ev.data;
    if (!m || m.id !== id) return;
    if (m.t === "http.head") status = m.status;
    else if (m.t === "http.data") parts.push(m.data);
    else if (m.t === "http.end") {
      clearTimeout(timer);
      w.removeEventListener("message", on);
      if (m.err) { reject(new Error(m.err)); return; }
      let n = 0;
      for (const p of parts) n += p.length;
      const all = new Uint8Array(n);
      let o = 0;
      for (const p of parts) { all.set(p, o); o += p.length; }
      let s = "";
      for (let i = 0; i < n; i += 8192) s += String.fromCharCode.apply(null, all.subarray(i, i + 8192));
      resolve(status + ":" + btoa(s));
    }
  }
  w.addEventListener("message", on);
  w.postMessage({ t: "http.req", id, method: "GET", url: "/containers/" + encodeURIComponent(name) + "/logs?stdout=1&stderr=1" });
})`, name)
	if err != nil {
		t.Fatalf("reading the logs of %s over the engine bridge: %v", name, err)
	}
	s, _ := v.(string)
	status, body, ok := strings.Cut(s, ":")
	if !ok || status != "200" {
		t.Fatalf("the logs of %s: status %q, want 200", name, status)
	}
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("the logs of %s are not base64: %v", name, err)
	}
	out, frames := demuxLogs(raw)
	t.Logf("the logs of %s over the bridge: %d bytes in %d frames, %d bytes of text", name, len(raw), frames, len(out))
	return out
}

// paneGameCounts is what the pane game's log holds of the audio summary lines,
// for the log: a window whose line is missing shows here as a count short of the
// windows the game printed.
func paneGameCounts(out string) string {
	flat := strings.NewReplacer("\r", "", "\n", "").Replace(out)
	return fmt.Sprintf("audiogaps: %d, audiodry: %d, audiotight: %d, slowtics: %d windows",
		strings.Count(flat, "audiogaps:"), strings.Count(flat, "audiodry "), strings.Count(flat, "audiotight "), strings.Count(flat, "slowtics:"))
}

func frame(stream byte, s string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
	return append(h, s...)
}

func TestDemuxLogsJoinsFramesAndKeepsATruncatedOne(t *testing.T) {
	var b []byte
	b = append(b, frame(1, "audiogaps: 3 of 9 writes ")...)
	b = append(b, frame(2, "ran dry\n")...)
	if got, n := demuxLogs(b); got != "audiogaps: 3 of 9 writes ran dry\n" || n != 2 {
		t.Errorf("demuxLogs = %q, %d frames", got, n)
	}
	cut := append(frame(1, "abc"), frame(1, "defgh")[:8+2]...)
	if got, n := demuxLogs(cut); got != "abcde" || n != 2 {
		t.Errorf("a truncated frame: demuxLogs = %q, %d frames, want %q, 2", got, n, "abcde")
	}
	if got, n := demuxLogs([]byte{1, 0, 0}); got != "" || n != 0 {
		t.Errorf("a stray header: demuxLogs = %q, %d", got, n)
	}
	// What the terminal cuts, the log keeps: a summary line longer than a screen row.
	long := "audiogaps: 1 of 2 writes ran dry, " + strings.Repeat("x", 300) + "; writes more than 50 100 200 ms after the one before: 1 0 0"
	got, _ := demuxLogs(frame(1, long+"\n"))
	if ls := slowTicLines(got); len(ls) != 1 {
		t.Errorf("slowTicLines on a long line of the log = %q", ls)
	}
	if c := paneGameCounts(got); !strings.Contains(c, "audiogaps: 1,") {
		t.Errorf("paneGameCounts = %q", c)
	}
}
