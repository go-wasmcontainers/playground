// Command tone plays a sine through the host module wctr_audio_out
// (go-wasmcontainers/engine docs/audio-design.md): the playground's sound
// test image. This file is the part that needs no host: the pacing and the
// generator, tested natively. main.go is the wasip1 guest.
package main

import (
	"math"
	"time"
)

// Stream parameters: 11025 Hz stereo like Quake's mixer, a ring of 2048 frames
// (186 ms), and 60 ms kept queued (design section 6: two frame intervals are
// the floor that survives one late frame; 50 to 100 ms is what a game keeps).
const (
	rate      = 11025
	channels  = 2
	capacity  = 2048
	target    = rate * 60 / 1000 // frames kept queued
	amplitude = 0.25
)

// pacer decides how many frames to write on each tic: min(elapsed * rate,
// target - queued), never more than the ring holds. This is tyrquake's
// S_Update (snd_dma.c): `queued` plays the part of the DMA position, and
// `elapsed` keeps time advancing while the stream waits for the page (a
// waiting stream reports queued 0, and would otherwise be handed a full
// target of frames every tic).
type pacer struct {
	rate, target, capacity int
	owed                   float64
}

// due returns the frames to write after elapsed, with queued frames in the
// ring.
func (p *pacer) due(elapsed time.Duration, queued int) int {
	p.owed += elapsed.Seconds() * float64(p.rate)
	if max := float64(p.target); p.owed > max {
		p.owed = max // a stall is not a debt: do not burst to catch up
	}
	n := int(p.owed)
	if room := p.target - queued; n > room {
		n = room
	}
	if n > p.capacity {
		n = p.capacity
	}
	if n < 0 {
		n = 0
	}
	return n
}

// wrote records frames the host accepted.
func (p *pacer) wrote(n int) { p.owed -= float64(n) }

// sine generates a continuous sine of hz at amplitude amp, interleaved on
// every channel; silent writes zeros (the planted negative).
type sine struct {
	hz     float64
	rate   int
	amp    float32
	silent bool
	phase  float64 // in cycles, [0, 1)
}

// fill writes frames of ch channels into dst (len(dst) >= frames*ch).
func (s *sine) fill(dst []float32, frames, ch int) {
	step := s.hz / float64(s.rate)
	for i := 0; i < frames; i++ {
		var v float32
		if !s.silent {
			v = s.amp * float32(math.Sin(2*math.Pi*s.phase))
		}
		for c := 0; c < ch; c++ {
			dst[i*ch+c] = v
		}
		s.phase += step
		if s.phase >= 1 {
			s.phase -= math.Floor(s.phase)
		}
	}
}
