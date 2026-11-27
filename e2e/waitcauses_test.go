//go:build e2e

package e2e

import (
	"math"
	"strings"
	"testing"

	"github.com/go-wasmcontainers/engine/runtime/browser"
)

// The measured causes of a wait (engine#90): the worker's event loop ran no
// timer (late tick, "worker not scheduled") or a message to it was still in
// flight. A wait is credited only for its overlap with a measured interval.

// causeEngineLog has one slice far from every wait, so the log is not empty and
// the waits fall in its holes.
var causeEngineLog = []browser.BusySlice{{At: 9000, Dur: 5, Trigger: "msg:call", Labels: []browser.BusyLabel{{Name: "call:x", Ms: 5}}}}

func causeShare(shares []browser.Share, cause string) float64 {
	for _, s := range shares {
		if s.Cause == cause {
			return s.Ms
		}
	}
	return 0
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func causeCalls() []hostCall {
	// one call that waited 60 ms, from 1000 to 1060
	return []hostCall{{"fd_pread", 1000, 70, 60, 8, 2, 0, 0, 0, 0}}
}

func TestWaitCoveredByLateTickIsAttributed(t *testing.T) {
	logs := busyLogs{Engine: causeEngineLog, EngineLate: []browser.Interval{{From: 990, To: 1070}}}
	shares, waited, un := waitAttribution(causeCalls(), logs)
	if !near(waited, 60) || !near(un, 0) || !near(causeShare(shares, browser.CauseNotScheduled), 60) {
		t.Errorf("waited %.1f unattributed %.1f shares %+v, want 60 / 0 / %q 60", waited, un, shares, browser.CauseNotScheduled)
	}
}

func TestWaitCoveredByTransitIsAttributed(t *testing.T) {
	logs := busyLogs{Engine: causeEngineLog, EngineTransits: []browser.Interval{{From: 1000, To: 1060}}}
	shares, _, un := waitAttribution(causeCalls(), logs)
	if !near(un, 0) || !near(causeShare(shares, browser.CauseInFlight), 60) {
		t.Errorf("unattributed %.1f shares %+v, want 0 / %q 60", un, shares, browser.CauseInFlight)
	}
}

func TestUncoveredWaitStaysUnexplained(t *testing.T) {
	logs := busyLogs{
		Engine:         causeEngineLog,
		EngineLate:     []browser.Interval{{From: 2000, To: 2100}},
		EngineTransits: []browser.Interval{{From: 3000, To: 3100}},
	}
	shares, _, un := waitAttribution(causeCalls(), logs)
	if !near(un, 60) || causeShare(shares, browser.CauseNotScheduled) != 0 || causeShare(shares, browser.CauseInFlight) != 0 {
		t.Errorf("unattributed %.1f shares %+v, want 60 and no measured cause", un, shares)
	}
}

func TestPartialOverlapCreditsOnlyTheOverlap(t *testing.T) {
	logs := busyLogs{
		Engine:         causeEngineLog,
		EngineLate:     []browser.Interval{{From: 980, To: 1020}},  // 20 ms inside the wait
		EngineTransits: []browser.Interval{{From: 1050, To: 1200}}, // 10 ms inside the wait
	}
	shares, _, un := waitAttribution(causeCalls(), logs)
	if g := causeShare(shares, browser.CauseNotScheduled); !near(g, 20) {
		t.Errorf("%q = %.1f, want 20: %+v", browser.CauseNotScheduled, g, shares)
	}
	if g := causeShare(shares, browser.CauseInFlight); !near(g, 10) {
		t.Errorf("%q = %.1f, want 10: %+v", browser.CauseInFlight, g, shares)
	}
	if !near(un, 30) {
		t.Errorf("unattributed %.1f, want 30", un)
	}
}

// The intervals credited are those of the worker that served the call.
func TestMeasuredIntervalsAreTheServingWorkers(t *testing.T) {
	calls := []hostCall{{"[method]context.ready-to-present (gpu host)", 1000, 70, 60, 8, 2, 0, 0, 0, 0}}
	logs := busyLogs{Engine: causeEngineLog, Host: causeEngineLog,
		EngineLate: []browser.Interval{{From: 1000, To: 1060}},
		HostLate:   []browser.Interval{{From: 1000, To: 1030}}}
	shares, _, un := waitAttribution(calls, logs)
	if g := causeShare(shares, browser.CauseNotScheduled); !near(g, 30) || !near(un, 30) {
		t.Errorf("late %.1f unattributed %.1f, want the host worker's 30 / 30: %+v", g, un, shares)
	}
}

func TestLongWaitsMessageNamesBothMeasuredCauses(t *testing.T) {
	calls := []hostCall{{"fd_pread", 1000, 130, 120, 8, 2, 0, 0, 0, 0}}
	logs := busyLogs{
		Engine:         causeEngineLog,
		EngineLate:     []browser.Interval{{From: 1000, To: 1010}},
		EngineTransits: []browser.Interval{{From: 1010, To: 1020}},
	}
	msg, bad := longWaitsLackLabels(calls, logs)
	if !bad || !strings.Contains(msg, "worker not scheduled 10 ms") || !strings.Contains(msg, "message in flight 10 ms") {
		t.Errorf("msg = %q bad = %v, want both causes with their ms", msg, bad)
	}
}
