//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// The sound e2e (docs/audio-design.md section 7): the tone image writes a sine
// through wctr_audio_out, the page plays it through an AudioWorklet, and an
// AnalyserNode tapped after the worklet measures what came out.
//
// Chromium runs with the desktop autoplay policy (a context starts only after
// a user gesture), as wasmbox's probe does: headless Chromium otherwise lets
// audio start by itself and "running after a click" would prove nothing.
//
// A reading is judged by verdict, which returns the assertions that FAIL. The
// positive run must fail none. Each planted negative must fail exactly the
// assertion it is built to trip (and the test fails if it passes, or fails for
// another reason), which proves each assertion can see what it checks.

const (
	audioWindow = 2 * time.Second
	// underrunBound is the stated bound on starvation events in the 2 s window
	// (the worklet counts one per starvation, then re-primes).
	underrunBound = 5
	rmsFloorDb    = -30.0 // a 0.25 sine is -15 dBFS; below -30 is silence for this test
	toneRate      = 11025 // the tone image's stream rate (played counts at the context's)
	fftSize       = 4096
)

// reading is one stream's __wctrAudio.stats() entry.
type reading struct {
	Stream    int     `json:"stream"`
	Container string  `json:"container"`
	Context   string  `json:"context"`
	Attached  bool    `json:"attached"`
	Played    uint32  `json:"played"`
	Underruns uint32  `json:"underruns"`
	Heartbeat uint32  `json:"heartbeat"`
	Buffered  uint32  `json:"buffered"`
	RmsDb     float64 `json:"rmsDb"`
	PeakBin   int     `json:"peakBin"`
	PeakHz    float64 `json:"peakHz"`
	BinHz     float64 `json:"binHz"`
}

// window is what two readings 2 s apart say.
type window struct {
	Rate                    float64 // the context's sample rate
	PlayedFrames, Underruns uint32
	Last                    reading
}

// verdict returns the failed assertions, by name: "played" (less than half of
// the window's worth of frames), "freq" (the peak is not the wanted bin plus or
// minus one), "rms" (quieter than rmsFloorDb), "underruns" (at the bound or
// over).
func verdict(w window, hz float64) []string {
	var fails []string
	if float64(w.PlayedFrames) < 0.5*w.Rate*audioWindow.Seconds() {
		fails = append(fails, "played")
	}
	if want := int(math.Round(hz * fftSize / w.Rate)); w.Last.PeakBin < want-1 || w.Last.PeakBin > want+1 {
		fails = append(fails, "freq")
	}
	if w.Last.RmsDb < rmsFloorDb {
		fails = append(fails, "rms")
	}
	if w.Underruns >= underrunBound {
		fails = append(fails, "underruns")
	}
	return fails
}

// The judge itself, on synthetic readings: each assertion trips on its own
// input and not on the others'. (Without this a verdict that always passed
// would pass every negative's twin.)
func TestAudioVerdict(t *testing.T) {
	good := window{Rate: 48000, PlayedFrames: 96000, Last: reading{RmsDb: -15, PeakBin: 38}}
	if f := verdict(good, 440); len(f) != 0 {
		t.Fatalf("a good window fails %v", f)
	}
	for _, c := range []struct {
		name string
		w    window
		want string
	}{
		{"half the frames", window{Rate: 48000, PlayedFrames: 47999, Last: good.Last}, "played"},
		{"no frames", window{Rate: 48000, PlayedFrames: 0, Last: good.Last}, "played"},
		{"another pitch", window{Rate: 48000, PlayedFrames: 96000, Last: reading{RmsDb: -15, PeakBin: 75}}, "freq"},
		{"silence", window{Rate: 48000, PlayedFrames: 96000, Last: reading{RmsDb: -200, PeakBin: 38}}, "rms"},
		{"starved", window{Rate: 48000, PlayedFrames: 96000, Underruns: underrunBound, Last: good.Last}, "underruns"},
	} {
		if f := verdict(c.w, 440); len(f) != 1 || f[0] != c.want {
			t.Errorf("%s: failed %v, want only %s", c.name, f, c.want)
		}
	}
	// One bin of slack either side, no more.
	for bin, ok := range map[int]bool{36: false, 37: true, 38: true, 39: true, 40: false} {
		g := good
		g.Last.PeakBin = bin
		if got := len(verdict(g, 440)) == 0; got != ok {
			t.Errorf("peak bin %d accepted=%v, want %v", bin, got, ok)
		}
	}
}

// quietPage drives the page WITHOUT giving it a user gesture. Playwright's own
// page.Evaluate runs with userGesture:true (so does typing: keydown is a
// gesture), and either would activate the page, let the AudioContext start by
// itself and make "running after a click" vacuous. This evaluates through the
// DevTools Protocol with userGesture:false, and "types" into the terminal by
// posting the keys to the engine worker, as wasmbox's probe does. Only
// click() is a real, trusted input event.
type quietPage struct {
	cdp playwright.CDPSession
}

func (p quietPage) Evaluate(js string, _ ...any) (any, error) {
	r, err := p.cdp.Send("Runtime.evaluate", map[string]any{
		"expression":    `(async () => { const f = (` + js + `); return typeof f === "function" ? f() : f; })()`,
		"returnByValue": true, "awaitPromise": true, "userGesture": false,
	})
	if err != nil {
		return nil, err
	}
	m, _ := r.(map[string]any)
	if ex, ok := m["exceptionDetails"]; ok {
		return nil, fmt.Errorf("%v", ex)
	}
	res, _ := m["result"].(map[string]any)
	return res["value"], nil
}

func (p quietPage) Keyboard() playwright.Keyboard { return quietKeys{p} }

type quietKeys struct{ p quietPage }

func (k quietKeys) send(data string) error {
	b, _ := json.Marshal(data)
	_, err := k.p.Evaluate(`() => { window.__wctrEngine.postMessage({ t: "input", data: ` + string(b) + ` }); return true; }`)
	return err
}

func (k quietKeys) Type(text string, _ ...playwright.KeyboardTypeOptions) error { return k.send(text) }
func (k quietKeys) Press(key string, _ ...playwright.KeyboardPressOptions) error {
	if key != "Enter" {
		return fmt.Errorf("quiet keyboard: key %q is not supported", key)
	}
	return k.send("\r")
}
func (k quietKeys) Down(string) error         { return fmt.Errorf("quiet keyboard: Down is not supported") }
func (k quietKeys) Up(string) error           { return fmt.Errorf("quiet keyboard: Up is not supported") }
func (k quietKeys) InsertText(s string) error { return k.send(s) }

type audioTab struct {
	tab
	page playwright.Page // for the one trusted input event: click
}

func newAudioBrowser(t *testing.T, pw *playwright.Playwright) playwright.Browser {
	t.Helper()
	b, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
		// Playwright adds --autoplay-policy=no-user-gesture-required by default.
		IgnoreDefaultArgs: []string{"--autoplay-policy=no-user-gesture-required"},
		Args:              []string{"--autoplay-policy=document-user-activation-required"},
		Timeout:           playwright.Float(180000),
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	return b
}

// freshTab is an isolated browser context (its own IndexedDB and engine lock)
// on the playground, ready.
func freshTab(t *testing.T, browser playwright.Browser, url, who string) audioTab {
	t.Helper()
	ctx, err := browser.NewContext()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctx.Close() })
	page, err := ctx.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := page.Goto(url + "?ui=terminal"); err != nil {
		t.Fatal(err)
	}
	cdp, err := ctx.NewCDPSession(page)
	if err != nil {
		t.Fatal(err)
	}
	a := audioTab{tab{t, quietPage{cdp}, who}, page}
	a.ready()
	return a
}

func (a audioTab) streams() []reading {
	a.t.Helper()
	raw, err := json.Marshal(a.eval(`() => window.__wctrAudio ? window.__wctrAudio.stats() : []`))
	if err != nil {
		a.t.Fatal(err)
	}
	var rs []reading
	if err := json.Unmarshal(raw, &rs); err != nil {
		a.t.Fatalf("%s: %v in %s", a.who, err, raw)
	}
	return rs
}

func (a audioTab) contextState() string {
	s, _ := a.eval(`() => window.__wctrAudio ? window.__wctrAudio.state() : "none"`).(string)
	return s
}

func (a audioTab) contextRate() float64 {
	return num(a.eval(`() => window.__wctrAudio ? window.__wctrAudio.contextRate() : 0`))
}

// measure reads the first stream twice, audioWindow apart.
func (a audioTab) measure() window {
	a.t.Helper()
	first := a.streams()
	if len(first) == 0 {
		a.t.Fatalf("%s: no stream to measure", a.who)
	}
	time.Sleep(audioWindow)
	last := a.streams()
	if len(last) == 0 {
		a.t.Fatalf("%s: the stream went away", a.who)
	}
	return window{
		Rate:         a.contextRate(),
		PlayedFrames: last[0].Played - first[0].Played,
		Underruns:    last[0].Underruns - first[0].Underruns,
		Last:         last[0],
	}
}

// start runs the tone container detached and returns once the page has its
// stream (or fails). args follow the image name.
func (a audioTab) start(name, flags, args string) {
	a.t.Helper()
	a.must("wctr run -d --name "+name+" "+flags+" tone "+args, 0)
	a.until("the page got no sound stream from "+name, `() => window.__wctrAudio && window.__wctrAudio.stats().some((s) => s.attached)`, 60*time.Second)
}

func (a audioTab) click() {
	a.t.Helper()
	// A trusted click (CDP Input.dispatchMouseEvent: user activation), on the
	// empty part of the page.
	if err := a.page.Mouse().Click(400, 300); err != nil {
		a.t.Fatal(err)
	}
}

func TestAudio(t *testing.T) {
	if os.Getenv("E2E_URL") != "" {
		t.Skip("builds its own site")
	}
	pw, err := playwright.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Stop()
	browser := newAudioBrowser(t, pw)
	defer browser.Close()
	url := serve(t, true)
	t.Logf("1-minute load %s", loadAvg())

	// 1. The positive: tone, with --audio, one click.
	a := freshTab(t, browser, url, "chromium/tone")
	a.start("tone1", "--audio", "--seconds 25")
	// The context is suspended before any gesture: otherwise the gesture check
	// below proves nothing. The worklet does not run, so the heartbeat is 0 and
	// nothing has been played; the host discards what the guest writes.
	if st := a.contextState(); st != "suspended" {
		t.Fatalf("the AudioContext is %q before any gesture (the autoplay policy is not enforced: the click check would be vacuous)", st)
	}
	time.Sleep(1500 * time.Millisecond)
	if r := a.streams()[0]; r.Played != 0 || r.Heartbeat != 0 || r.Buffered != 0 {
		t.Fatalf("before the click: played %d heartbeat %d buffered %d, want all 0", r.Played, r.Heartbeat, r.Buffered)
	}
	a.click()
	a.until("the AudioContext did not run after a trusted click", `() => window.__wctrAudio.state() === "running"`, 5*time.Second)
	a.until("nothing was played after the click", `() => window.__wctrAudio.stats().some((s) => s.played > 0)`, 15*time.Second)
	time.Sleep(500 * time.Millisecond) // let the stream prime
	w := a.measure()
	t.Logf("positive: context %.0f Hz; %d frames played in %v; %d underruns; peak bin %d = %.1f Hz (bin %.2f Hz); rms %.1f dBFS; heartbeat %d; buffered %d",
		w.Rate, w.PlayedFrames, audioWindow, w.Underruns, w.Last.PeakBin, w.Last.PeakHz, w.Last.BinHz, w.Last.RmsDb, w.Last.Heartbeat, w.Last.Buffered)
	if f := verdict(w, 440); len(f) != 0 {
		t.Errorf("the 440 Hz tone fails %v", f)
	}
	if w.Last.Heartbeat == 0 {
		t.Error("the worklet's heartbeat never moved")
	}
	lat, _ := json.Marshal(a.eval(`() => window.__wctrAudio.latency()`))
	t.Logf("platform latency (s): %s", lat)

	// Volume and mute, per container and page-wide: the level that reaches the
	// speakers (after both gains) follows them, while the tap before the gains
	// still hears the tone.
	out := func() float64 { return num(a.eval(`() => window.__wctrAudio.out().rmsDb`)) }
	until := func(what string, ok func(float64) bool) float64 {
		var db float64
		for i := 0; i < 100; i++ {
			if db = out(); ok(db) {
				return db
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("%s: speakers at %.1f dBFS", what, db)
		return db
	}
	loud := until("full volume", func(db float64) bool { return db > rmsFloorDb })
	// Per container: the dashboard's pane calls setContainer (TestQuakeAudioPane
	// drives the real pane buttons); this page has no dashboard, so the entry
	// point is called directly.
	cname, _ := a.eval(`() => window.__wctrAudio.containers()[0].container`).(string)
	setc := func(v float64, muted bool) {
		a.eval(fmt.Sprintf(`() => { window.wctrAudio.setContainer(%q, %g, %v); return true; }`, cname, v, muted))
	}
	setc(0.3, false)
	// The analyser holds 4096 samples (0.5 s at 8 kHz), so wait for the old ones
	// to leave: the level settles at 20*log10(0.3) = -10.46 dB below full.
	want30 := loud + 20*math.Log10(0.3)
	half := until("volume 30%", func(db float64) bool { return math.Abs(db-want30) < 1.5 })
	t.Logf("speakers: %.1f dBFS at full volume, %.1f at 30%% (expect %.1f)", loud, half, want30)
	setc(0.3, true)
	until("container mute", func(db float64) bool { return db < -60 })
	if g := a.eval(`() => window.__wctrAudio.containers()[0].gain`); num(g) != 0 {
		t.Errorf("a muted container's gain is %v", g)
	}
	setc(0.3, false) // unmute
	until("container unmute", func(db float64) bool { return db > -60 })
	setc(1, false)
	until("volume back", func(db float64) bool { return db > rmsFloorDb })
	a.eval(`() => { document.getElementById("soundmute").click(); return true; }`)
	until("page mute", func(db float64) bool { return db < -60 })
	if tap := a.streams()[0]; tap.RmsDb < rmsFloorDb {
		t.Errorf("the pre-gain tap went quiet (%.1f dBFS) with the page muted: the worklet must keep playing", tap.RmsDb)
	}
	if g := num(a.eval(`() => window.__wctrAudio.pageGain()`)); g != 0 {
		t.Errorf("a page-muted gain is %v", g)
	}
	a.eval(`() => { document.getElementById("soundmute").click(); return true; }`)
	until("page unmute", func(db float64) bool { return db > rmsFloorDb })

	// The cost of a stream_write, from the guest's own clock: read the summary
	// once it has run its 25 s.
	var log string
	for i := 0; i < 40 && !strings.Contains(log, "stream_write calls"); i++ {
		_, log = a.run("wctr logs tone1")
		if !strings.Contains(log, "stream_write calls") {
			time.Sleep(2 * time.Second)
		}
	}
	if m := regexp.MustCompile(`(\d+) stream_write calls, mean (\d+) us`).FindStringSubmatch(log); m != nil {
		t.Logf("stream_write: %s calls, mean %s us per call (one engine round trip each)", m[1], m[2])
	} else {
		t.Errorf("tone printed no summary:\n%s", log)
	}
	if m := regexp.MustCompile(`played (\d+) underruns (\d+) refused (\d+) state (\w+)`).FindStringSubmatch(log); m != nil {
		t.Logf("guest's own stats: played %s underruns %s refused %s state %s", m[1], m[2], m[3], m[4])
	}
	if !strings.Contains(log, "440 Hz, 11025 Hz stereo, amplitude 0.25") {
		t.Errorf("tone did not say what it plays:\n%s", log)
	}
	a.page.Close()

	// 2. Planted negatives, each in a fresh tab, each judged by the same verdict.
	negatives := []struct {
		name, flags, args string
		hz                float64
		click             bool
		wantFail          []string // these assertions must fail ...
		wantPass          []string // ... and these must NOT (the negative trips only its own)
	}{
		// zeros: the frames play and nothing is starved, but the tone is not
		// there (silence has no peak either, so "freq" fails with it: not asserted).
		{"silent", "--audio", "--silent --seconds 12", 440, true, []string{"rms"}, []string{"played", "underruns"}},
		// another pitch: the level is right and the frames play, the peak is in another bin
		{"880", "--audio", "--hz 880 --seconds 12", 440, true, []string{"freq"}, []string{"played", "rms", "underruns"}},
		// no click: the context never leaves suspended, nothing is played
		{"noclick", "--audio", "--seconds 12", 440, false, []string{"played"}, nil},
	}
	for _, n := range negatives {
		n := n
		t.Run("negative/"+n.name, func(t *testing.T) {
			a := freshTab(t, browser, url, "chromium/tone-"+n.name)
			a.start("neg", n.flags, n.args)
			if n.click {
				a.click()
				a.until("the AudioContext did not run after a click", `() => window.__wctrAudio.state() === "running"`, 5*time.Second)
				a.until("nothing was played", `() => window.__wctrAudio.stats().some((s) => s.played > 0)`, 15*time.Second)
				time.Sleep(500 * time.Millisecond)
			} else {
				// No gesture: it stays suspended for as long as we look.
				time.Sleep(3 * time.Second)
				if st := a.contextState(); st != "suspended" {
					t.Fatalf("without a click the context is %q, want suspended", st)
				}
			}
			w := a.measure()
			got := verdict(w, n.hz)
			t.Logf("%s: failed %v; played %d, peak bin %d, rms %.1f dBFS, heartbeat %d", n.name, got, w.PlayedFrames, w.Last.PeakBin, w.Last.RmsDb, w.Last.Heartbeat)
			if len(got) == 0 {
				t.Fatalf("the planted negative %q PASSES the 440 Hz assertions: the test cannot see what it checks", n.name)
			}
			has := func(s string) bool { return slices.Contains(got, s) }
			for _, f := range n.wantFail {
				if !has(f) {
					t.Errorf("the negative %q was meant to fail %q but failed %v", n.name, f, got)
				}
			}
			for _, p := range n.wantPass {
				if has(p) {
					t.Errorf("the negative %q failed %q, which it must not (it failed %v)", n.name, p, got)
				}
			}
			if !n.click && (w.PlayedFrames != 0 || w.Last.Played != 0 || w.Last.Heartbeat != 0) {
				t.Errorf("no click, yet played %d (total %d) heartbeat %d", w.PlayedFrames, w.Last.Played, w.Last.Heartbeat)
			}
		})
	}

	// 3. Without --audio the host answers Denied: tone says so and exits 2, the
	// page never gets a stream, and nothing is played, click or no click.
	t.Run("negative/no--audio", func(t *testing.T) {
		a := freshTab(t, browser, url, "chromium/tone-denied")
		a.must("wctr run --rm tone --seconds 2", 2, "stream_open: denied")
		a.click()
		time.Sleep(500 * time.Millisecond)
		if n := len(a.streams()); n != 0 {
			t.Errorf("the page has %d stream(s) for a container without --audio", n)
		}
		if st := a.contextState(); st != "none" {
			t.Errorf("the page made an AudioContext (%s) for a container without --audio", st)
		}
	})

	// 4. Sanity of the unit: a stream the guest never closes ends with the
	// container, and the page forgets it.
	b := freshTab(t, browser, url, "chromium/tone-end")
	b.start("end", "--audio", "--seconds 3")
	b.until("the stream outlived its container", `() => window.__wctrAudio.stats().length === 0`, 30*time.Second)
	if bar, _ := b.eval(`() => !!document.getElementById("soundbar")`).(bool); bar {
		t.Error("the Sound bar is still shown after the last stream closed")
	}
}

// loadAvg is the 1-minute load, for the log: underrun counts are not a
// measurement on a loaded machine.
func loadAvg() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "unknown"
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return "unknown"
	}
	if v, err := strconv.ParseFloat(f[0], 64); err == nil && v > 8 {
		return f[0] + " (above 8: underrun counts are not a measurement)"
	}
	return f[0]
}
