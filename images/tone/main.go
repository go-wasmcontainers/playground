//go:build wasip1

// Command tone plays a 440 Hz sine, 11025 Hz stereo, amplitude 0.25, through
// the host module wctr_audio_out: the playground's sound test image.
//
//	wctr run -d --audio tone [--hz N] [--silent] [--seconds N]
//
// It needs `--audio` (without it the host answers Denied and tone exits 2) and,
// for the page to play it, one click on the page: until then the stream is
// "waiting" and the host discards what is written, which tone tolerates (it
// keeps pacing by the clock). --hz N changes the pitch and --silent writes
// zeros: the planted negatives of the e2e test. It prints what the host said
// at the end, including the mean cost of a stream_write call.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
	"unsafe"
)

//go:wasmimport wctr_audio_out stream_open
func streamOpen(rate, channels, capacity uint32, out unsafe.Pointer) uint32

//go:wasmimport wctr_audio_out stream_write
func streamWrite(stream, ptr, frames uint32, out unsafe.Pointer) uint32

//go:wasmimport wctr_audio_out stream_stats
func streamStats(stream uint32, out unsafe.Pointer) uint32

//go:wasmimport wctr_audio_out stream_close
func streamClose(stream uint32) uint32

// Errno values of the module that tone says in words.
var errnoName = map[uint32]string{1: "invalid argument", 2: "denied (run the container with --audio)", 3: "unsupported (this host has no audio output)",
	4: "too large", 5: "quota exceeded", 9: "fault"}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tone [--hz N] [--silent] [--seconds N]")
	os.Exit(2)
}

func main() {
	hz, seconds, silent := 440.0, 20.0, false
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--silent":
			silent = true
		case "--hz", "--seconds":
			i++
			if i >= len(os.Args) {
				usage()
			}
			v, err := strconv.ParseFloat(os.Args[i], 64)
			if err != nil || v <= 0 || v > 5000 && os.Args[i-1] == "--hz" || v > 600 && os.Args[i-1] == "--seconds" {
				usage()
			}
			if os.Args[i-1] == "--hz" {
				hz = v
			} else {
				seconds = v
			}
		default:
			usage()
		}
	}

	var handle uint32
	if e := streamOpen(rate, channels, capacity, unsafe.Pointer(&handle)); e != 0 {
		fmt.Printf("tone: stream_open: %s\n", errnoName[e])
		os.Exit(2)
	}
	fmt.Printf("tone: %g Hz, %d Hz stereo, amplitude %g, silent=%v\n", hz, rate, amplitude, silent)

	g := &sine{hz: hz, rate: rate, amp: amplitude, silent: silent}
	p := &pacer{rate: rate, target: target, capacity: capacity}
	pcm := make([]float32, capacity*channels)
	var res [2]uint32
	var calls int
	var callTime time.Duration
	var written int
	write := func(frames int) (queued uint32) {
		t0 := time.Now()
		e := streamWrite(handle, uint32(uintptr(unsafe.Pointer(&pcm[0]))), uint32(frames), unsafe.Pointer(&res))
		callTime += time.Since(t0)
		calls++
		if e != 0 {
			fmt.Printf("tone: stream_write: %s\n", errnoName[e])
			os.Exit(2)
		}
		return res[1]
	}

	end := time.Now().Add(time.Duration(seconds * float64(time.Second)))
	last := time.Now()
	for time.Now().Before(end) {
		time.Sleep(10 * time.Millisecond)
		now := time.Now()
		elapsed := now.Sub(last)
		last = now
		queued := int(write(0)) // a poll: the host reports what is queued
		n := p.due(elapsed, queued)
		if n == 0 {
			continue
		}
		g.fill(pcm, n, channels)
		write(n)
		p.wrote(int(res[0]))
		written += int(res[0])
	}

	var st [4]uint32
	if e := streamStats(handle, unsafe.Pointer(&st)); e != 0 {
		fmt.Printf("tone: stream_stats: %s\n", errnoName[e])
		os.Exit(2)
	}
	states := [...]string{"waiting", "playing", "closed"}
	state := "?"
	if st[3] < uint32(len(states)) {
		state = states[st[3]]
	}
	fmt.Printf("tone: wrote %d frames; played %d underruns %d refused %d state %s\n", written, st[0], st[1], st[2], state)
	fmt.Printf("tone: %d stream_write calls, mean %d us\n", calls, callTime.Microseconds()/int64(max(calls, 1)))
	streamClose(handle)
}
