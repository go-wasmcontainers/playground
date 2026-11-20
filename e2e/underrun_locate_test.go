//go:build e2e

package e2e

import (
	"fmt"
	"regexp"
	"strconv"
	"testing"

	"github.com/mxschmitt/playwright-go"
)

// The page's underrun probe: every 5 ms it reads the stream's counters and
// notes, with the wall clock (Date.now(): the guest's time.Now() is the same
// clock), each increase of the underrun count, and what the ring held a moment
// before. Installed with watchUnderruns, read with takeUnderruns.
const underrunProbeJS = `() => {
  if (window.__ur) return true;
  const ur = (window.__ur = { ev: [], last: 0, buf: [] });
  setInterval(() => {
    const a = window.__wctrAudio && window.__wctrAudio.stats()[0];
    if (!a) return;
    ur.buf.push(a.buffered); if (ur.buf.length > 8) ur.buf.shift();
    if (a.underruns > ur.last) { ur.ev.push({ wall: Date.now(), n: a.underruns - ur.last, total: a.underruns, before: ur.buf.slice(), played: a.played }); ur.last = a.underruns; }
  }, 5);
  return true;
}`

// underrunEvent is one increase of the page's underrun count.
type underrunEvent struct {
	Wall   int64
	N      int
	Total  int
	Before []float64
}

func watchUnderruns(t *testing.T, page playwright.Page) {
	t.Helper()
	if _, err := page.Evaluate(underrunProbeJS); err != nil {
		t.Fatal(err)
	}
}

func takeUnderruns(t *testing.T, page playwright.Page) []underrunEvent {
	t.Helper()
	v, err := page.Evaluate(`() => window.__ur ? window.__ur.ev : []`)
	if err != nil {
		t.Fatal(err)
	}
	var out []underrunEvent
	list, _ := v.([]any)
	for _, e := range list {
		m, _ := e.(map[string]any)
		ev := underrunEvent{Wall: int64(num(m["wall"])), N: int(num(m["n"])), Total: int(num(m["total"]))}
		if b, ok := m["before"].([]any); ok {
			for _, x := range b {
				ev.Before = append(ev.Before, num(x))
			}
		}
		out = append(out, ev)
	}
	return out
}

// dryGap is one audiodry line of the game: the wall time (Unix ms) its next write
// began, the gap since the last returned, the queue it held, and the two sites.
type dryGap struct {
	Start            int64
	Gap, Held, Short float64
	From, To         string
}

var dryRe = regexp.MustCompile(`audiodry unix-ms=(\d+) gap-ms=([0-9.]+) queued-ms=([0-9.]+) short-ms=([0-9.]+) from=([a-z-]*#\d+) to=([a-z-]*#\d+)`)

func parseDryGaps(lines []string) []dryGap {
	var out []dryGap
	for _, l := range lines {
		m := dryRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		ms, _ := strconv.ParseInt(m[1], 10, 64)
		g, _ := strconv.ParseFloat(m[2], 64)
		h, _ := strconv.ParseFloat(m[3], 64)
		s, _ := strconv.ParseFloat(m[4], 64)
		out = append(out, dryGap{ms, g, h, s, m[5], m[6]})
	}
	return out
}

// matchUnderruns says, for each underrun the page counted, which dry gap of the
// game contains it (the gap runs from Start-Gap to Start, the page's clock read
// every 5 ms: 30 ms of slack either side), and which dry gaps no underrun of the
// page falls in: the two accounts, side by side. It returns the number of
// underruns with no dry gap and of dry gaps with no underrun.
func matchUnderruns(t *testing.T, evs []underrunEvent, gaps []dryGap) (orphanUnderruns, orphanGaps int) {
	t.Helper()
	const slack = 30
	used := make([]bool, len(gaps))
	for _, e := range evs {
		found := -1
		for i, g := range gaps {
			if float64(e.Wall) >= float64(g.Start)-g.Gap-slack && e.Wall <= g.Start+slack {
				found = i
				break
			}
		}
		if found < 0 {
			orphanUnderruns++
			t.Logf("UNDERRUN +%d (total %d) at %d: no dry gap of the game holds it; the ring held %v mix frames in the 40 ms before", e.N, e.Total, e.Wall, e.Before)
			continue
		}
		used[found] = true
		g := gaps[found]
		t.Logf("UNDERRUN +%d (total %d) at %d: the game's gap of %.0f ms on %.0f ms queued (short %.0f ms), writes %s -> %s, the next one at %d (%d ms later); the ring held %v mix frames in the 40 ms before",
			e.N, e.Total, e.Wall, g.Gap, g.Held, g.Short, g.From, g.To, g.Start, g.Start-e.Wall, e.Before)
	}
	for i := range gaps {
		if !used[i] {
			orphanGaps++
		}
	}
	t.Logf("UNDERRUN account: the page counted %d underrun events, the game %d dry gaps; %d underruns with no dry gap, %d dry gaps with no underrun", len(evs), len(gaps), orphanUnderruns, orphanGaps)
	return orphanUnderruns, orphanGaps
}

var _ = fmt.Sprintf

// An underrun of the page falls in the dry gap of the game that spans its time
// (the gap ends where the next write began and runs back by its length); one
// outside every gap, and a gap no underrun falls in, are counted apart.
func TestMatchUnderruns(t *testing.T) {
	gaps := parseDryGaps([]string{
		"audiodry unix-ms=100000 gap-ms=250.0 queued-ms=99.0 short-ms=151.0 from=tic-end#10 to=present-wait#11",
		"audiodry unix-ms=200000 gap-ms=120.0 queued-ms=99.0 short-ms=21.0 from=tic-end#40 to=tic-end#41",
	})
	if len(gaps) != 2 || gaps[0].From != "tic-end#10" || gaps[0].Start != 100000 || gaps[1].Short != 21 {
		t.Fatalf("parsed %+v", gaps)
	}
	evs := []underrunEvent{{Wall: 99900, N: 1, Total: 1}, {Wall: 150000, N: 1, Total: 2}}
	oe, og := matchUnderruns(t, evs, gaps)
	if oe != 1 || og != 1 {
		t.Errorf("orphan underruns %d, orphan gaps %d; want 1 and 1", oe, og)
	}
}

// The engine worker's event-loop lag probe: the guest's every host call (a sound
// write, a poll_oneoff sleep, each call of a frame) is a message to this worker's
// event loop, so a loop that is busy for 40 ms delays the guest's call by 40 ms.
// A 2 ms timer is read back: how late it ran says how long the loop was busy,
// with the wall-clock time of each late one (Date.now(), the clock of the page's
// underruns and of the game's audiodry lines).
const loopLagProbeJS = `() => {
  if (self.__lag) return true;
  const lag = (self.__lag = { ev: [], n: 0, last: 0 });
  const tick = () => {
    const now = performance.now();
    if (lag.last) {
      const late = now - lag.last - 2;
      lag.n++;
      if (late > 15) lag.ev.push({ wall: Date.now(), late });
    }
    lag.last = now;
    setTimeout(tick, 2);
  };
  setTimeout(tick, 2);
  return true;
}`

// lagEvent is one late tick of the worker's loop.
type lagEvent struct {
	Wall int64
	Late float64
}

func watchLoopLag(t *testing.T, w playwright.Worker) {
	t.Helper()
	if _, err := w.Evaluate(loopLagProbeJS); err != nil {
		t.Fatal(err)
	}
}

func takeLoopLag(t *testing.T, w playwright.Worker) (events []lagEvent, ticks int) {
	t.Helper()
	v, err := w.Evaluate(`() => ({ ev: self.__lag ? self.__lag.ev : [], n: self.__lag ? self.__lag.n : 0 })`)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := v.(map[string]any)
	list, _ := m["ev"].([]any)
	for _, e := range list {
		x, _ := e.(map[string]any)
		events = append(events, lagEvent{Wall: int64(num(x["wall"])), Late: num(x["late"])})
	}
	return events, int(num(m["n"]))
}

// lagNear returns the worker-loop stalls that ended within 150 ms before wall.
func lagNear(events []lagEvent, wall int64) []lagEvent {
	var out []lagEvent
	for _, e := range events {
		if e.Wall <= wall+30 && e.Wall >= wall-150 {
			out = append(out, e)
		}
	}
	return out
}
