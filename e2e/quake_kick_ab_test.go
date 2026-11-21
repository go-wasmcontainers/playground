//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// presentStalls are the context.present host calls of 90 ms or more the game's
// lines name (draw-slowest=[method]context.present:MS, the longest host call of a
// present Draw, and world-slowest= for the world draw's): how many, and the
// longest. Firefox polls its GPU process every 100 ms (POLL_TIME_MS), so a
// 90-110 ms call is a wait for that timer, which the host library's kicks
// (go-gfx/webgpu kick.go: empty queue.submit([]) while a GPU wait is pending)
// exist to avoid.
func presentStalls(lines []string) (n int, longest float64) {
	for _, l := range lines {
		for _, m := range regexp.MustCompile(`(?:draw|world)-slowest=\S*present\S*?:([0-9.]+)`).FindAllStringSubmatch(l, -1) {
			ms, _ := strconv.ParseFloat(m[1], 64)
			if ms >= 90 {
				n++
			}
			longest = max(longest, ms)
		}
	}
	return n, longest
}

// TestQuakeFirefoxKickAB asks whether our own queue traffic contributes to the
// Firefox context.present stall (about 100 ms, attempt 1 of playground#63): the
// same paced run (quake e1m1, 300 frames, --audio) in a page whose engine worker
// drops the kicks (?nokick=1, loopback pages only) and in one that does not,
// interleaved (kicks, none, none, kicks, ...) so the host's drift lands on both.
// The variable is the kicks; the metrics are the game's frame rate, its dry gaps
// and the present stalls of 90 ms or more. The control is the arm with kicks:
// a nokick page that did not drop any would read like it. It only measures, and
// asserts that every arm ran and printed its lines (E2E_KICK_AB_ROUNDS rounds of
// the two arms, default 2).
func TestQuakeFirefoxKickAB(t *testing.T) {
	if os.Getenv("E2E_KICK_AB") != "1" {
		t.Skip("set E2E_KICK_AB=1 (an A/B of the host library's kicks in the real Firefox)")
	}
	rounds := 2
	if s := os.Getenv("E2E_KICK_AB_ROUNDS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			t.Fatalf("E2E_KICK_AB_ROUNDS=%q", s)
		}
		rounds = n
	}
	type row struct {
		arm         string
		fps         float64
		dry, stalls int
		longest     float64
	}
	var rows []row
	for r := 0; r < rounds; r++ {
		arms := []string{"kicks", "nokick"}
		if r%2 == 1 {
			arms[0], arms[1] = arms[1], arms[0]
		}
		for _, arm := range arms {
			query := "?ui=terminal"
			if arm == "nokick" {
				query += "&nokick=1"
			}
			b, logPath, stop := firefoxTabAt(t, query)
			ff, ok := b.page.(ffPage)
			if !ok {
				stop()
				t.Fatalf("the Firefox tab is a %T", b.page)
			}
			if err := ff.s.Click(ff.ctx, 400, 300); err != nil {
				stop()
				t.Fatal(err)
			}
			out := b.must("wctr run --rm --gpu --audio -m 512m quake --map e1m1 --frames 300", 0, "audio: playing")
			fps, _, _, _, found := stallsLine(out)
			lines := slowTicLines(out)
			if !found || len(lines) == 0 {
				t.Errorf("round %d, %s: no stall accounting or no lines (firefox output tail:\n%s)", r, arm, fileTail(logPath, 3000))
			}
			dry := 0
			for _, l := range lines {
				if strings.HasPrefix(l, "audiodry ") {
					dry++
				}
			}
			n, longest := presentStalls(lines)
			rows = append(rows, row{arm, fps, dry, n, longest})
			t.Logf("round %d %-6s: %.1f fps, %d dry gaps, %d present stalls >= 90 ms, longest %.1f ms; %s", r, arm, fps, dry, n, longest, ticTail(out))
			stop()
		}
	}
	for _, arm := range []string{"kicks", "nokick"} {
		var fps, longest float64
		var dry, stalls, n int
		for _, x := range rows {
			if x.arm == arm {
				fps += x.fps
				dry += x.dry
				stalls += x.stalls
				longest = max(longest, x.longest)
				n++
			}
		}
		if n > 0 {
			t.Log(fmt.Sprintf("kickab %-6s: %d runs, mean %.1f fps, %d dry gaps, %d present stalls >= 90 ms, longest %.1f ms", arm, n, fps/float64(n), dry, stalls, longest))
		}
	}
}

func TestPresentStallsCountsTheLongPresentCalls(t *testing.T) {
	n, longest := presentStalls([]string{
		"audiodry unix-ms=1 gap-ms=1.0 queued-ms=1.0 short-ms=1.0 from=tic-end#2 to=world#3 draw-slowest=[method]context.present:101.9 world-gpu-ms=1.0 world-copy-ms=1.0 world-slowest=queue.submit:89.9",
		"audiodry unix-ms=2 gap-ms=1.0 queued-ms=1.0 short-ms=1.0 from=tic-end#3 to=world#4 draw-slowest=[method]context.present:12.0",
	})
	if n != 1 || longest != 101.9 {
		t.Errorf("presentStalls = %d, %.1f", n, longest)
	}
}
