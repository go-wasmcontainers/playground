//go:build e2e

package e2e

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/mxschmitt/playwright-go"
)

// hostCall is one slow call of a guest to the engine, as the guest worker split
// it (engine runtime/browser worker.js): the epoch time (ms) it was posted, its
// total, and the parts: the wait for the engine's thread to begin it, the engine
// serving it, the return trip.
type hostCall struct {
	Name                   string
	At                     float64
	Total, Wait, Exec, Ret float64
}

// takeHostSlow reads the engine worker's list of slow calls (self.__hostSlow).
func takeHostSlow(t *testing.T, w playwright.Worker) []hostCall {
	t.Helper()
	v, err := w.Evaluate(`() => (self.__hostSlow || []).map((m) => ({ name: m.name, at: m.at, total: m.total, wait: m.wait, exec: m.exec, ret: m.ret }))`)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := v.([]any)
	var out []hostCall
	for _, e := range list {
		x, _ := e.(map[string]any)
		name, _ := x["name"].(string)
		out = append(out, hostCall{name, num(x["at"]), num(x["total"]), num(x["wait"]), num(x["exec"]), num(x["ret"])})
	}
	return out
}

// hostSlowReport is the split of the slow calls, for the log: the totals of each
// part over all, and the slowest calls one by one. Which part dominates says
// where the time goes: waiting for the engine's thread, the browser's API, or the
// way back.
func hostSlowReport(calls []hostCall, show int) []string {
	if len(calls) == 0 {
		return []string{"host calls: none took 10 ms or more"}
	}
	var wait, exec, ret, total float64
	for _, c := range calls {
		wait, exec, ret, total = wait+c.Wait, exec+c.Exec, ret+c.Ret, total+c.Total
	}
	out := []string{fmt.Sprintf("host calls: %d took 10 ms or more, %.0f ms in all: waiting for the engine %.0f ms (%.0f%%), executing %.0f ms (%.0f%%), returning %.0f ms (%.0f%%)",
		len(calls), total, wait, 100*wait/total, exec, 100*exec/total, ret, 100*ret/total)}
	sorted := slices.Clone(calls)
	slices.SortFunc(sorted, func(a, b hostCall) int {
		switch {
		case a.Total > b.Total:
			return -1
		case a.Total < b.Total:
			return 1
		}
		return 0
	})
	for _, c := range sorted[:min(show, len(sorted))] {
		out = append(out, fmt.Sprintf("host call at %.0f: %s total %.1f ms = wait %.1f + exec %.1f + return %.1f", c.At, c.Name, c.Total, c.Wait, c.Exec, c.Ret))
	}
	return out
}

func TestHostSlowReportSplitsTheTotal(t *testing.T) {
	if got := hostSlowReport(nil, 5); len(got) != 1 || !strings.Contains(got[0], "none took") {
		t.Errorf("no calls: %q", got)
	}
	calls := []hostCall{
		{"[method]gpu-queue.submit", 1000, 40, 30, 8, 2},
		{"[method]gpu-command-encoder.finish", 2000, 60, 10, 45, 5},
	}
	got := hostSlowReport(calls, 1)
	if len(got) != 2 {
		t.Fatalf("report = %q, want the summary and the 1 slowest", got)
	}
	if !strings.Contains(got[0], "2 took 10 ms or more, 100 ms in all: waiting for the engine 40 ms (40%), executing 53 ms (53%), returning 7 ms (7%)") {
		t.Errorf("summary = %q", got[0])
	}
	if !strings.Contains(got[1], "finish total 60.0 ms = wait 10.0 + exec 45.0 + return 5.0") {
		t.Errorf("slowest = %q", got[1])
	}
}
