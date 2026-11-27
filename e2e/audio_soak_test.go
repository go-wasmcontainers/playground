//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// What the user hears is the worklet's underruns: the frames it was asked for
// and the ring did not hold. A soak watches the stream from its first sound to
// the end and FAILS on any underrun; the game's frame rate, the rate at which
// the mixer fills the ring and the ring's fill are logged beside it (and kept as
// a JSON line in E2E_AUDIO_REPORT, uploaded by CI) so that a slow lane can be
// told from a broken mixer by numbers, not by a looser bound.
type soundSoak struct {
	lane           string
	t0             time.Time
	first, last    reading
	minBuf, maxBuf uint32
	fps            []float64
	line           []soakPoint
}

func newSoak(lane string, r reading) *soundSoak {
	return &soundSoak{lane: lane, t0: time.Now(), first: r, last: r, minBuf: r.Buffered, maxBuf: r.Buffered}
}

// note records one reading of the stream, and once a second a point of the
// timeline: when the underruns happen against the ring's fill is what tells a
// slow tic from a drift.
func (s *soundSoak) note(r reading) {
	s.last = r
	s.minBuf, s.maxBuf = min(s.minBuf, r.Buffered), max(s.maxBuf, r.Buffered)
	if t := time.Since(s.t0).Seconds(); len(s.line) == 0 || t-s.line[len(s.line)-1].T >= 1 {
		s.line = append(s.line, soakPoint{T: t, Buffered: r.Buffered, Underruns: r.Underruns - s.first.Underruns,
			Written: r.Written - s.first.Written, Played: r.Played - s.first.Played})
	}
}

// soakPoint is the stream at one second of a soak.
type soakPoint struct {
	T         float64 `json:"t"`
	Buffered  uint32  `json:"buffered"`
	Underruns uint32  `json:"underruns"` // since the soak began
	Written   uint32  `json:"written"`   // frames the mixer wrote since the soak began
	Played    uint32  `json:"played"`    // frames the worklet consumed since the soak began
}

// noteFPS records the game's frame rate as the lane measured it.
func (s *soundSoak) noteFPS(f float64) { s.fps = append(s.fps, f) }

// soakResult is one soak's numbers side by side.
type soakResult struct {
	Lane         string  `json:"lane"`
	Seconds      float64 `json:"seconds"`
	FPS          float64 `json:"fps"`            // the game's frame rate (mean), 0 if not measured
	MixRatio     float64 `json:"mixRatio"`       // frames the mixer wrote into the ring per second over the stream's rate
	Underruns    uint32  `json:"underruns"`      // since the soak began
	UnderrunsAll uint32  `json:"underrunsTotal"` // since the stream opened (the start-up included)
	BufferedMin  uint32  `json:"bufferedMin"`
	BufferedMax  uint32  `json:"bufferedMax"`
	// Timeline is one point a second; Played over Written shows whether the
	// worklet consumed faster than the mixer wrote (a drift) or the mixer paused
	// (a slow tic).
	Timeline []soakPoint `json:"timeline"`
}

func (s *soundSoak) result() soakResult {
	secs := time.Since(s.t0).Seconds()
	res := soakResult{Lane: s.lane, Seconds: secs,
		Underruns: s.last.Underruns - s.first.Underruns, UnderrunsAll: s.last.Underruns,
		BufferedMin: s.minBuf, BufferedMax: s.maxBuf, Timeline: s.line}
	if secs > 0 && s.first.Rate > 0 {
		res.MixRatio = float64(s.last.Written-s.first.Written) / (secs * s.first.Rate)
	}
	for _, f := range s.fps {
		res.FPS += f / float64(len(s.fps))
	}
	return res
}

// soakVerdict names what a soak's numbers fail: the underruns the user hears.
func soakVerdict(r soakResult) []string {
	var bad []string
	if r.Underruns > 0 {
		bad = append(bad, fmt.Sprintf("the audio underran %d times in %.0f s (at %.1f frames a second, the mixer filling %.3f of real time, the ring holding %d to %d frames)",
			r.Underruns, r.Seconds, r.FPS, r.MixRatio, r.BufferedMin, r.BufferedMax))
	}
	return bad
}

// streamReading is the page's first stream, read through Playwright.
func streamReading(t *testing.T, page playwright.Page) reading {
	t.Helper()
	v, err := page.Evaluate(`() => window.__wctrAudio ? window.__wctrAudio.stats() : []`)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	var rs []reading
	if err := json.Unmarshal(raw, &rs); err != nil || len(rs) == 0 {
		return reading{}
	}
	return rs[0]
}

// tabReading is the first stream of a tab driven through WebDriver.
func tabReading(b tab) reading {
	b.t.Helper()
	raw, _ := json.Marshal(b.eval(`() => window.__wctrAudio ? window.__wctrAudio.stats() : []`))
	var rs []reading
	if err := json.Unmarshal(raw, &rs); err != nil || len(rs) == 0 {
		return reading{}
	}
	return rs[0]
}

var fpsRE = regexp.MustCompile(`(\d+) fps`)

// paneFPS is the frame rate the dashboard's pane shows ("1280x720, 30 fps").
func paneFPS(pane string) (float64, bool) {
	m := fpsRE.FindStringSubmatch(pane)
	if m == nil {
		return 0, false
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	return f, true
}

// finish logs the numbers, keeps them for CI, and fails on an underrun.
func (s *soundSoak) finish(t *testing.T) {
	t.Helper()
	r := s.result()
	t.Logf("SOUND %s: %.0f s, game %.1f fps, mixer %.3f of real time, underruns %d (%d since the stream opened), ring %d..%d frames",
		r.Lane, r.Seconds, r.FPS, r.MixRatio, r.Underruns, r.UnderrunsAll, r.BufferedMin, r.BufferedMax)
	if path := os.Getenv("E2E_AUDIO_REPORT"); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			line, _ := json.Marshal(r)
			fmt.Fprintf(f, "%s\n", line)
			f.Close()
		}
	}
	for _, b := range soakVerdict(r) {
		t.Error(b)
	}
}

// The verdict fails an underrun and passes a clean soak: the planted negative.
func TestSoakVerdict(t *testing.T) {
	for _, c := range []struct {
		name string
		r    soakResult
		bad  bool
	}{
		{"clean", soakResult{Seconds: 65, FPS: 30, MixRatio: 1.0, BufferedMin: 300, BufferedMax: 1100}, false},
		{"startup underruns only", soakResult{Seconds: 65, UnderrunsAll: 40}, false},
		{"one underrun", soakResult{Seconds: 65, Underruns: 1}, true},
		{"many", soakResult{Seconds: 65, FPS: 9, MixRatio: 0.7, Underruns: 120}, true},
	} {
		if got := len(soakVerdict(c.r)) > 0; got != c.bad {
			t.Errorf("%s: failed=%v, want %v", c.name, got, c.bad)
		}
	}
	s := newSoak("x", reading{Underruns: 5, Written: 0, Rate: 11025})
	s.note(reading{Underruns: 5, Written: 11025, Buffered: 500, Rate: 11025})
	if r := s.result(); r.Underruns != 0 || r.UnderrunsAll != 5 || r.BufferedMin != 0 || r.BufferedMax != 500 {
		t.Errorf("a stream opened with 5 underruns and none since: %+v", r)
	}
	s.note(reading{Underruns: 7, Buffered: 100})
	if r := s.result(); r.Underruns != 2 {
		t.Errorf("2 underruns since the soak began: %+v", r)
	}
}
