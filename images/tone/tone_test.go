package main

import (
	"math"
	"testing"
	"time"
)

// A guest that writes what the pacer allows tracks elapsed*rate within a tic
// and never queues more than the target, whatever the tics are like.
func TestPacerTracksTheClock(t *testing.T) {
	p := &pacer{rate: rate, target: target, capacity: capacity}
	queued, played, written := 0, 0, 0
	elapsedTotal := time.Duration(0)
	tics := []time.Duration{10, 17, 16, 40, 8, 33, 5, 12, 100, 16, 16}
	for k := 0; k < 300; k++ {
		e := tics[k%len(tics)] * time.Millisecond
		elapsedTotal += e
		// the page consumes in real time
		play := int(e.Seconds() * rate)
		if play > queued {
			play = queued
		}
		queued -= play
		played += play
		n := p.due(e, queued)
		if n < 0 || n > capacity || queued+n > target {
			t.Fatalf("tic %d: wrote %d on %d queued (target %d)", k, n, queued, target)
		}
		p.wrote(n)
		queued += n
		written += n
	}
	// Frames written track elapsed*rate within one target, except what a stall
	// longer than the target costs on purpose (a stall is not a debt: the
	// pacer drops the excess rather than bursting).
	want, dropped := elapsedTotal.Seconds()*rate, 0.0
	for k := 0; k < 300; k++ {
		if over := (tics[k%len(tics)]*time.Millisecond).Seconds()*rate - target; over > 0 {
			dropped += over
		}
	}
	if math.Abs(float64(written)-(want-dropped)) > float64(target)+1 {
		t.Errorf("wrote %d frames over %.2f s, expected about %.0f (%.0f dropped to stalls)", written, elapsedTotal.Seconds(), want-dropped, dropped)
	}
}

// While the stream waits for the page (queued stays 0, the host discards), the
// pacer still hands out frames at the clock's rate, not a full target a tic.
func TestPacerWhileWaiting(t *testing.T) {
	p := &pacer{rate: rate, target: target, capacity: capacity}
	total := 0
	for k := 0; k < 100; k++ {
		n := p.due(10*time.Millisecond, 0)
		p.wrote(n)
		total += n
	}
	if want := 1.0 * rate; math.Abs(float64(total)-want) > 5 {
		t.Errorf("a waiting stream was handed %d frames in 1 s, want about %.0f", total, want)
	}
	// A stall is not a debt.
	if n := p.due(10*time.Second, 0); n > target {
		t.Errorf("after a 10 s stall: %d frames, more than the target %d", n, target)
	}
}

func TestSine(t *testing.T) {
	s := &sine{hz: 440, rate: rate, amp: amplitude}
	buf := make([]float32, 2*rate)
	s.fill(buf, rate, 2)
	var sum float64
	for k := 0; k < rate; k++ {
		if buf[2*k] != buf[2*k+1] {
			t.Fatal("channels differ")
		}
		if math.Abs(float64(buf[2*k])) > amplitude+1e-6 {
			t.Fatalf("sample %d = %v above the amplitude", k, buf[2*k])
		}
		sum += float64(buf[2*k]) * float64(buf[2*k])
	}
	if rms := math.Sqrt(sum / rate); math.Abs(rms-amplitude/math.Sqrt2) > 1e-3 {
		t.Errorf("rms = %v, want %v", rms, amplitude/math.Sqrt2)
	}
	// 440 cycles in one second: count rising zero crossings.
	cross := 0
	for k := 1; k < rate; k++ {
		if buf[2*(k-1)] < 0 && buf[2*k] >= 0 {
			cross++
		}
	}
	if cross < 439 || cross > 441 {
		t.Errorf("%d cycles in 1 s, want 440", cross)
	}
	// Continuous across calls (no phase reset).
	a := &sine{hz: 440, rate: rate, amp: amplitude}
	b := &sine{hz: 440, rate: rate, amp: amplitude}
	x, y := make([]float32, 2*1000), make([]float32, 2*1000)
	a.fill(x, 1000, 2)
	b.fill(y[:2*300], 300, 2)
	b.fill(y[2*300:], 700, 2)
	for k := range x {
		if x[k] != y[k] {
			t.Fatalf("sample %d differs when written in two calls", k)
		}
	}
}

func TestSilent(t *testing.T) {
	s := &sine{hz: 440, rate: rate, amp: amplitude, silent: true}
	buf := make([]float32, 2*100)
	for k := range buf {
		buf[k] = 1
	}
	s.fill(buf, 100, 2)
	for k, v := range buf {
		if v != 0 {
			t.Fatalf("silent sample %d = %v", k, v)
		}
	}
}
