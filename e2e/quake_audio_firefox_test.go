//go:build e2e

package e2e

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// TestQuakeAudioFirefox is the Chromium soak's numbers in the real Firefox
// (the lane of TestQuakeFirefox): the quake image with --audio, its mixer's
// accounting after 300 frames, then 65 s of the stream played by the page, with
// the underruns the user would hear, the game's frame rate, the mixer's rate and
// the ring's fill logged side by side (E2E_AUDIO_REPORT). Firefox's silent-run
// ratios of 0.68 and 0.82 (the soak lane's logs of playground#66) are explained
// the same way: by the stalled tics the game now counts.
func TestQuakeAudioFirefox(t *testing.T) {
	secs := 65
	if s := os.Getenv("E2E_SOAK_SECONDS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			t.Fatalf("E2E_SOAK_SECONDS=%q", s)
		}
		secs = n
	}
	b, logPath, stop := firefoxTab(t)
	defer stop()
	defer func() {
		if t.Failed() {
			t.Logf("firefox output tail:\n%s", fileTail(logPath, 6000))
		}
	}()
	// A trusted click, so the AudioContext starts as soon as a stream opens.
	ff, ok := b.page.(ffPage)
	if !ok {
		t.Fatalf("the Firefox tab is a %T", b.page)
	}
	if err := ff.s.Click(ff.ctx, 400, 300); err != nil {
		t.Fatal(err)
	}

	out := b.must("wctr run --rm --gpu --audio -m 512m quake --map e1m1 --frames 300", 0, "audio: playing")
	fps, dropped, stalls, withStalls, found := stallsLine(out)
	if !found {
		t.Fatalf("the game's audio line has no stall accounting (go-quake1/engine#133):\n%s", out)
	}
	t.Logf("firefox paced run: game %.1f fps, %d mix frames dropped in %d stalled tics, ratio with them %.3f; %s", fps, dropped, stalls, withStalls, ticTail(out))
	for _, l := range slowTicLines(out) {
		t.Log(l)
	}
	if withStalls < pacedAccountedMin || withStalls > pacedRatioMax(300/fps) {
		t.Errorf("the mixer offered %.3f of the game's time with its stalls added back, want %.2f to %.3f", withStalls, pacedAccountedMin, pacedRatioMax(300/fps))
	}

	b.must("wctr run -d --gpu --audio -m 512m --name qa quake --map e1m1", 0)
	b.until("the page got no sound stream from quake", `() => window.__wctrAudio && window.__wctrAudio.stats().some((s) => s.attached)`, 90*time.Second)
	b.until("the AudioContext is not running", `() => window.__wctrAudio.state() === "running"`, 20*time.Second)
	b.until("quake's stream played nothing", `() => window.__wctrAudio.stats().some((s) => s.played > 0)`, 30*time.Second)
	time.Sleep(2 * time.Second)
	sk := newSoak("firefox/quake", tabReading(b))
	sk.noteFPS(fps)
	requireSignal(t, "firefox quake", secs, func() float64 {
		r := tabReading(b)
		sk.note(r)
		return r.Rms
	})
	sk.finish(t)
	b.must("wctr rm -f qa", 0)
}
