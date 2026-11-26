//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/go-wasmcontainers/engine/runtime/browser"
)

// attempt1Calls are the slow calls of run 45696648860 attempt 1 (job
// 139495687911, quake_audio_pane_test.go:378) that waited, as the log recorded
// them: the gpu host calls served by the GPU host worker, and the one wait the
// engine worker caused (fd_pread behind a msg:http.cancel slice, 31 ms).
func attempt1Calls() []hostCall {
	return []hostCall{
		{"wasi-gfx:surface/surface-webgpu@0.2.0 [method]context.ready-to-present (gpu host)", 1795639537574, 69.1, 67.8, 0.4, 0.8, 0, 0, 1, 0},
		{"wasi-gfx:surface/surface-webgpu@0.2.0 [method]context.submit-present-end (gpu host)", 1795639578501, 68.2, 64.4, 1.3, 2.6, 0, 0, 1, 0},
		{"wasi-gfx:surface/surface-webgpu@0.2.0 [method]context.ready-to-present (gpu host)", 1795639492109, 16.0, 14.4, 1.5, 0.1, 0, 0, 1, 0},
		{"wasi-gfx:surface/surface-webgpu@0.2.0 [method]context.ready-to-present (gpu host)", 1795639575081, 14.9, 13.2, 0.3, 1.4, 0, 0, 1, 0},
		{"wasi-gfx:surface/surface-webgpu@0.2.0 [method]context.ready-to-present (gpu host)", 1795639548805, 12.0, 11.7, 0.2, 0.1, 0, 0, 1, 0},
		{"wasi_snapshot_preview1 fd_pread", 1795639487585, 31.6, 31.3, 0.1, 0.3, 0, 0, 1, 0},
	}
}

// The engine worker's log of attempt 1 held the msg:http.cancel slice behind
// the fd_pread; the host worker's log was not read (the defect), so the slices
// below that cover the gpu host waits stand for what the host worker was doing:
// serving earlier calls of the same guest (msg:call).
func attempt1Logs() (engine, host []browser.BusySlice) {
	engine = []browser.BusySlice{{At: 1795639487585, Dur: 30.7, Trigger: "msg:http.cancel"}}
	for _, c := range attempt1Calls() {
		if strings.HasSuffix(c.Name, " (gpu host)") {
			host = append(host, browser.BusySlice{At: c.At - 1, Dur: c.Wait + 2, Trigger: "msg:call"})
		}
	}
	return engine, host
}

func TestAttempt1WaitsAreExplainedByTheHostWorkersLog(t *testing.T) {
	engine, host := attempt1Logs()
	if msg, bad := judgeWaits(attempt1Calls(), engine, host); bad {
		t.Errorf("the waits of attempt 1 are covered by the logs of the workers that served them, flagged: %s", msg)
	}
}

// A gpu host call whose wait the host worker's log does not cover is still a hole.
func TestAGpuHostWaitWithNoHostSliceIsReported(t *testing.T) {
	engine, host := attempt1Logs()
	if _, bad := judgeWaits(attempt1Calls(), engine, nil); !bad {
		t.Error("gpu host waits with no host log were passed")
	}
	if _, bad := judgeWaits(attempt1Calls(), engine, host[:1]); !bad {
		t.Error("gpu host waits mostly outside the host log were passed")
	}
}
