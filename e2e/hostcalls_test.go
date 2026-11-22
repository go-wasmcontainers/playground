//go:build e2e

package e2e

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/go-wasmcontainers/engine/runtime/browser"
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
	// The return trip split (engine worker.js, v0.46.0): from the engine's answer to
	// the guest worker's Atomics.wait coming back (Wake: the browser's wake-up), and
	// from there to the call returning (Post: the guest's own code, the realloc
	// rounds), with the wake-ups without an answer and the realloc rounds.
	Wake, Post      float64
	Wakes, Reallocs int
}

// batchSub is one slow submit of the wasi_webgpu_batch module as the host
// module saw it (gpu v0.21.0, self.__batchSlow): when it ended (epoch ms, the
// engine worker's clock), how long its frame ran and waited for the GPU's turn,
// and what the frame held.
type batchSub struct {
	At, Ran, Waited          float64
	Bytes, Commands, Creates int
	Made                     string // each creation: kind, descriptor bytes, provider ms (gpu v0.22.0)
}

// takeBatchSlow reads the engine worker's list of slow submits.
func takeBatchSlow(t *testing.T, w playwright.Worker) []batchSub {
	t.Helper()
	v, err := w.Evaluate("() => (self.__batchSlow || []).map((m) => ({ at: m.at, ran: m.ran, waited: m.waited, bytes: m.bytes, commands: m.commands, creates: m.creates, made: (m.made || []).map((k) => k.kind + ' ' + k.bytes + 'B ' + k.ran + 'ms').join(', ') }))")
	if err != nil {
		t.Fatal(err)
	}
	list, _ := v.([]any)
	var out []batchSub
	for _, e := range list {
		x, _ := e.(map[string]any)
		out = append(out, batchSub{num(x["at"]), num(x["ran"]), num(x["waited"]), int(num(x["bytes"])), int(num(x["commands"])), int(num(x["creates"])), fmt.Sprint(x["made"])})
	}
	return out
}

// batchFor is the slow submit that ended inside the host call c (its window
// widened by 5 ms for the two clocks), if there is one.
func batchFor(c hostCall, subs []batchSub) (batchSub, bool) {
	for _, s := range subs {
		if s.At >= c.At && s.At <= c.At+c.Total+5 {
			return s, true
		}
	}
	return batchSub{}, false
}

// takeHostSlow reads the engine worker's list of slow calls (self.__hostSlow).
// Since engine v0.42.0 the times are on the engine worker's clock (the guest's
// offset, "off", is already taken out), the clock of the busy log.
func takeHostSlow(t *testing.T, w playwright.Worker) []hostCall {
	t.Helper()
	v, err := w.Evaluate(`() => (self.__hostSlow || []).map((m) => ({ name: m.name, at: m.at, total: m.total, wait: m.wait, exec: m.exec, ret: m.ret, wake: m.wake, post: m.post, wakes: m.wakes, reallocs: m.reallocs }))`)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := v.([]any)
	var out []hostCall
	for _, e := range list {
		x, _ := e.(map[string]any)
		name, _ := x["name"].(string)
		out = append(out, hostCall{name, num(x["at"]), num(x["total"]), num(x["wait"]), num(x["exec"]), num(x["ret"]), num(x["wake"]), num(x["post"]), int(num(x["wakes"])), int(num(x["reallocs"]))})
	}
	return out
}

// takeBusy reads what the engine worker was doing (engine runtime/browser
// busy.js): every slice of its work of 1 ms or more, with its trigger and the
// Go sites' labels, beside self.__hostSlow. A page without the log (an engine
// before v0.42.0) gives none, and the report says so.
func takeBusy(t *testing.T, w playwright.Worker) []browser.BusySlice {
	t.Helper()
	v, err := w.Evaluate(`() => (self.__busy ? self.__busy.slices(1) : [])`)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := v.([]any)
	var out []browser.BusySlice
	for _, e := range list {
		x, _ := e.(map[string]any)
		s := browser.BusySlice{At: num(x["at"]), Dur: num(x["dur"]), GC: int(num(x["gc"])), GCMs: num(x["gcms"])}
		s.Trigger, _ = x["trigger"].(string)
		ls, _ := x["labels"].([]any)
		for _, l := range ls {
			m, _ := l.(map[string]any)
			name, _ := m["name"].(string)
			s.Labels = append(s.Labels, browser.BusyLabel{Name: name, Ms: num(m["ms"])})
		}
		out = append(out, s)
	}
	return out
}

// waitAttribution attributes the wait of every slow call (the time before the
// engine's thread began it) to what the engine worker was doing then: the
// shares by cause, largest first, and the part of the waits nothing names.
func waitAttribution(calls []hostCall, busy []browser.BusySlice) (shares []browser.Share, waited, unattributed float64) {
	by := map[string]float64{}
	for _, c := range calls {
		if c.Wait <= 0 {
			continue
		}
		waited += c.Wait
		unattributed += browser.Unattributed(c.At, c.At+c.Wait, busy)
		for _, s := range browser.Explain(c.At, c.At+c.Wait, busy) {
			by[s.Cause] += s.Ms
		}
	}
	for cause, ms := range by {
		shares = append(shares, browser.Share{Cause: cause, Ms: ms})
	}
	slices.SortFunc(shares, func(a, b browser.Share) int {
		switch {
		case a.Ms > b.Ms:
			return -1
		case a.Ms < b.Ms:
			return 1
		}
		return strings.Compare(a.Cause, b.Cause)
	})
	return shares, waited, unattributed
}

// busyReport is the wait attribution for the log: where the time the guest
// waited for the engine went, by cause.
func busyReport(calls []hostCall, slices []browser.BusySlice, show int) []string {
	if len(slices) == 0 {
		return []string{"busy log: none (the engine worker has no busy log)"}
	}
	shares, waited, un := waitAttribution(calls, slices)
	if waited == 0 {
		return []string{fmt.Sprintf("busy log: %d slices of 1 ms or more, no slow call waited", len(slices))}
	}
	out := []string{fmt.Sprintf("busy log: %d slices of 1 ms or more; the slow calls waited %.0f ms in all, %.0f ms (%.0f%%) under no label", len(slices), waited, un, 100*un/waited)}
	for _, s := range shares[:min(show, len(shares))] {
		out = append(out, fmt.Sprintf("busy wait %.0f ms (%.0f%%): %s", s.Ms, 100*s.Ms/waited, s.Cause))
	}
	// The longest slices themselves: what the engine's thread did for tens of
	// milliseconds at a time, with the time under each label and the GCs.
	longest := append([]browser.BusySlice(nil), slices...)
	sort.SliceStable(longest, func(i, j int) bool { return longest[i].Dur > longest[j].Dur })
	for _, s := range longest[:min(6, len(longest))] {
		var ls []string
		for _, l := range s.Labels {
			ls = append(ls, fmt.Sprintf("%s %.1f", l.Name, l.Ms))
		}
		out = append(out, fmt.Sprintf("busy slice at %.0f: %.1f ms, %s, gc %d, labels [%s]", s.At, s.Dur, s.Trigger, s.GC, strings.Join(ls, "; ")))
	}
	return out
}

// gcSplitLine is the long busy slices split by whether a garbage collection ran
// in them (browser.GCSplit): how many, how long, how much under no label.
func gcSplitLine(slices []browser.BusySlice, minDur float64) string {
	g := browser.GCSplit(slices, minDur)
	f := func(c browser.SliceClass) string {
		return fmt.Sprintf("%d slices, %.0f ms, %.0f ms unlabelled, %.0f ms of GC CPU, longest %.0f ms", c.N, c.Ms, c.Unlabelled, c.GCMs, c.Max)
	}
	return fmt.Sprintf("GC split of the slices of %.0f ms or more: with a collection: %s; without: %s", minDur, f(g.With), f(g.Without))
}

func TestGCSplitLine(t *testing.T) {
	got := gcSplitLine([]browser.BusySlice{{Dur: 33, GC: 1, GCMs: 20}, {Dur: 12}}, 10)
	want := "GC split of the slices of 10 ms or more: with a collection: 1 slices, 33 ms, 33 ms unlabelled, 20 ms of GC CPU, longest 33 ms; without: 1 slices, 12 ms, 12 ms unlabelled, 0 ms of GC CPU, longest 12 ms"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// longWaitsLackLabels says whether the slow calls' waits are mostly unexplained:
// with 100 ms or more of wait in all, more than half under no label is a hole in
// the log (the engine worker was busy with something this log cannot see), and
// the engine's thread is then not shown to be the cause of anything.
func longWaitsLackLabels(calls []hostCall, slices []browser.BusySlice) (string, bool) {
	if len(slices) == 0 {
		return "", false
	}
	_, waited, un := waitAttribution(calls, slices)
	if waited < 100 || un <= waited/2 {
		return "", false
	}
	return fmt.Sprintf("the slow host calls waited %.0f ms for the engine, %.0f ms (%.0f%%) of it under no label: the busy log does not see what occupied the engine worker", waited, un, 100*un/waited), true
}

// hostSlowReport is the split of the slow calls, for the log: the totals of each
// part over all, and the slowest calls one by one. Which part dominates says
// where the time goes: waiting for the engine's thread, the browser's API, or the
// way back.
func hostSlowReport(calls []hostCall, show int, subs ...batchSub) []string {
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
		line := fmt.Sprintf("host call at %.0f: %s total %.1f ms = wait %.1f + exec %.1f + return %.1f (browser wake-up %.1f + guest code %.1f; %d wake-ups, %d reallocs)", c.At, c.Name, c.Total, c.Wait, c.Exec, c.Ret, c.Wake, c.Post, c.Wakes, c.Reallocs)
		if b, ok := batchFor(c, subs); ok {
			line += fmt.Sprintf("; its frame: %d bytes, %d commands (%d creates), ran %.1f ms after waiting %.1f ms for the GPU's turn; creations [%s]", b.Bytes, b.Commands, b.Creates, b.Ran, b.Waited, b.Made)
		}
		out = append(out, line)
	}
	return out
}

func TestHostSlowReportSplitsTheTotal(t *testing.T) {
	if got := hostSlowReport(nil, 5); len(got) != 1 || !strings.Contains(got[0], "none took") {
		t.Errorf("no calls: %q", got)
	}
	calls := []hostCall{
		{"[method]gpu-queue.submit", 1000, 40, 30, 8, 2, 0, 0, 0, 0},
		{"[method]gpu-command-encoder.finish", 2000, 60, 10, 45, 5, 0, 0, 0, 0},
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

func TestHostSlowReportNamesTheReturnTripAndTheBatch(t *testing.T) {
	calls := []hostCall{{"wasi_webgpu_batch submit", 1000, 380, 3, 373, 4, 1.5, 2.5, 1, 0}}
	subs := []batchSub{{1376, 370, 2, 20480, 311, 0}, {9000, 50, 0, 1, 1, 0}}
	got := hostSlowReport(calls, 1, subs...)
	if len(got) != 2 {
		t.Fatalf("report = %q", got)
	}
	for _, want := range []string{"browser wake-up 1.5 + guest code 2.5; 1 wake-ups, 0 reallocs", "its frame: 20480 bytes, 311 commands (0 creates), ran 370.0 ms after waiting 2.0 ms"} {
		if !strings.Contains(got[1], want) {
			t.Errorf("%q lacks %q", got[1], want)
		}
	}
	// A submit that ended outside the call's window is not its frame.
	if b, ok := batchFor(calls[0], subs[1:]); ok {
		t.Errorf("matched a submit that ended at 9000: %+v", b)
	}
}

func TestLongWaitsLackLabels(t *testing.T) {
	calls := []hostCall{
		{"[method]gpu-queue.submit", 1000, 90, 80, 8, 2, 0, 0, 0, 0},
		{"clock_time_get", 2000, 60, 55, 3, 2, 0, 0, 0, 0},
	}
	named := []browser.BusySlice{
		{At: 990, Dur: 100, Trigger: "msg:call", Labels: []browser.BusyLabel{{Name: "call:gpu.queue-submit", Ms: 95}}},
		{At: 1990, Dur: 70, Trigger: "msg:http.req GET /containers/q/logs"},
	}
	if msg, bad := longWaitsLackLabels(calls, named); bad {
		t.Errorf("waits covered by named slices were flagged: %s", msg)
	}
	// The same waits with the worker busy under nothing the log names.
	bare := []browser.BusySlice{
		{At: 990, Dur: 100, Trigger: "timer"},
		{At: 1990, Dur: 70, Trigger: "callback"},
	}
	if msg, bad := longWaitsLackLabels(calls, bare); !bad || !strings.Contains(msg, "no label") {
		t.Errorf("waits under bare timers were not flagged: %q %v", msg, bad)
	}
	// And with no slice at all in the waits (the worker was busy outside Go).
	if _, bad := longWaitsLackLabels(calls, []browser.BusySlice{{At: 5000, Dur: 3, Trigger: "timer"}}); !bad {
		t.Error("waits no slice covers were not flagged")
	}
	// No busy log at all is reported by the log, not failed here.
	if _, bad := longWaitsLackLabels(calls, nil); bad {
		t.Error("a missing busy log was flagged as a hole in it")
	}
	if got := busyReport(calls, named, 3); len(got) < 2 || !strings.Contains(got[0], "under no label") {
		t.Errorf("report = %q", got)
	}
	// The longest slice is listed with its trigger and labels.
	all := strings.Join(busyReport(calls, bare, 3), "\n")
	if !strings.Contains(all, "busy slice at 990: 100.0 ms, timer") {
		t.Errorf("the longest slice is not listed: %s", all)
	}
}
