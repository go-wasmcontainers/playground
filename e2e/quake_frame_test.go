//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/mxschmitt/playwright-go"
)

// TestQuakeFrameTime (E2E_GPU=1 and E2E_QUAKE_BENCH=1) plays the quake image on
// e1m1 for 300 frames on a real GPU, in the image's default mode, and prints the
// mean frame and its phases as one line of Go benchmark output, so that abbench
// can alternate two builds of this test binary (two engine versions) and
// benchstat compare them:
//
//	BenchmarkQuakeFrame 1 <ns/frame> ns/op <ms> frame-ms <ms> models-ms ...
//
// models-ms is the software alias pass (0 once the GPU draws the entities), gpu-ms
// the GPU world's submit with its read-back.
func TestQuakeFrameTime(t *testing.T) {
	if os.Getenv("E2E_GPU") != "1" || os.Getenv("E2E_QUAKE_BENCH") != "1" || os.Getenv("E2E_URL") != "" {
		t.Skip("set E2E_GPU=1 and E2E_QUAKE_BENCH=1 on a lane with WebGPU")
	}
	pw, err := playwright.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Stop()
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true), Channel: playwright.String("chromium"),
		Args:    append([]string{"--enable-unsafe-webgpu"}, strings.Fields(os.Getenv("E2E_GPU_ARGS"))...),
		Timeout: playwright.Float(180000)})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer browser.Close()
	page, err := browser.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	b := tab{t, page, "chromium/quake-frame"}
	if _, err := page.Goto(serve(t, true) + "?ui=terminal"); err != nil {
		t.Fatal(err)
	}
	b.ready()
	out := b.must("wctr run --rm --gpu -m 512m quake --map e1m1 --frames 300", 0, "300 frames in ")
	get := func(name string) float64 {
		m := regexp.MustCompile(name + `=([0-9.]+)`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("no %s in:\n%s", name, out)
		}
		v, _ := strconv.ParseFloat(m[1], 64)
		return v
	}
	frame := get("frame-ms")
	fmt.Printf("BenchmarkQuakeFrame 1 %.0f ns/op %.4f frame-ms %.4f models-ms %.4f gpu-ms %.4f world-ms %.4f sim-ms %.4f present-ms\n",
		frame*1e6, frame, get("models-ms"), get("gpu-ms"), get("world-ms"), get("sim-ms"), get("present-ms"))
}
