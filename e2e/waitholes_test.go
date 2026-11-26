//go:build e2e

package e2e

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/go-wasmcontainers/engine/runtime/browser"
)

// loopLags are the late ticks of each worker's event loop (watchLoopLag).
type loopLags struct{ Engine, Host []lagEvent }

func (l loopLags) of(c hostCall) []lagEvent {
	if strings.HasSuffix(c.Name, gpuHostSuffix) {
		return l.Host
	}
	return l.Engine
}

// waitHoles says, for each slow call whose wait has 20 ms or more under no
// recorded slice, where the serving worker's log stops and starts again around
// the wait and which stalls of that worker's event loop (a 2 ms timer that ran
// 15 ms or more late) overlap it: a wait nothing in the busy log names is either
// the thread running something the log does not time (short slices that add up,
// browser work outside Go) or the thread not running at all (it was not
// scheduled), and the loop's stalls tell the second from the first.
func waitHoles(calls []hostCall, logs busyLogs, lags loopLags) []string {
	var out []string
	for _, c := range calls {
		if c.Wait <= 0 {
			continue
		}
		from, to := c.At, c.At+c.Wait
		log := logs.of(c)
		var hole float64
		for _, s := range browser.Explain(from, to, log) {
			if s.Cause == "no slice" {
				hole = s.Ms
			}
		}
		if hole < 20 {
			continue
		}
		before, after := -1.0, -1.0 // gaps to the nearest slice end before and start after
		for _, s := range log {
			if end := s.At + s.Dur; end <= from && (before < 0 || from-end < before) {
				before = from - end
			}
			if s.At >= to && (after < 0 || s.At-to < after) {
				after = s.At - to
			}
		}
		prevEnd, nextAt := "none", "none"
		if before >= 0 {
			prevEnd = fmt.Sprintf("%.1f", before)
		}
		if after >= 0 {
			nextAt = fmt.Sprintf("%.1f", after)
		}
		var stalls []string
		for _, e := range lags.of(c) {
			start, end := float64(e.Wall)-e.Late, float64(e.Wall)
			if end >= from-5 && start <= to+5 {
				stalls = append(stalls, fmt.Sprintf("%.0f ms late, ended at %d (%.0f ms into the wait)", e.Late, e.Wall, float64(e.Wall)-from))
			}
		}
		slices.Sort(stalls)
		out = append(out, fmt.Sprintf("wait hole: %s at %.0f waited %.1f ms, %.1f under no slice; the log's last slice ended %s ms before the wait, its next began %s ms after it; the worker's loop stalls there: [%s]",
			c.Name, c.At, c.Wait, hole, prevEnd, nextAt, strings.Join(stalls, "; ")))
	}
	return out
}

func TestWaitHolesNamesTheStallsAndTheLogsEdges(t *testing.T) {
	calls := []hostCall{
		{"[method]context.ready-to-present (gpu host)", 1000, 140, 138, 1, 1, 0, 0, 0, 0},
		{"fd_pread", 5000, 60, 5, 3, 2, 0, 0, 0, 0},
	}
	host := []browser.BusySlice{{At: 900, Dur: 90, Trigger: "msg:call"}, {At: 1200, Dur: 5, Trigger: "msg:call"}}
	lags := loopLags{Host: []lagEvent{{Wall: 1130, Late: 120}, {Wall: 9000, Late: 50}}}
	got := waitHoles(calls, busyLogs{Host: host}, lags)
	if len(got) != 1 {
		t.Fatalf("got %q, want one hole (the short fd_pread wait is covered by nothing but is under 20 ms)", got)
	}
	for _, want := range []string{"ready-to-present (gpu host) at 1000 waited 138.0 ms", "ended 10.0 ms before the wait", "began 62.0 ms after it", "120 ms late, ended at 1130 (130 ms into the wait)"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("%q lacks %q", got[0], want)
		}
	}
	if strings.Contains(got[0], "9000") {
		t.Errorf("a stall far from the wait was listed: %q", got[0])
	}
}
