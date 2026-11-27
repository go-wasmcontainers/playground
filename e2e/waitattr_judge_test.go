//go:build e2e

package e2e

import "github.com/go-wasmcontainers/engine/runtime/browser"

// judgeWaits is the pane test's judgement: each call against the log of the
// worker that served it.
func judgeWaits(calls []hostCall, engine, host []browser.BusySlice) (string, bool) {
	return longWaitsLackLabels(calls, busyLogs{Engine: engine, Host: host})
}
