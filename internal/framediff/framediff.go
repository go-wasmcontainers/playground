// Package framediff measures how much a picture moved sideways between two
// frames: the playground's Quake e2e turns the view with the mouse and checks
// that the scene moves the way the mouse says.
package framediff

import "fmt"

// Shift returns s, in pixels, such that frame b is frame a moved s pixels to
// the right (s < 0: to the left; 0: not moved or no better match). a and b
// are w x h RGBA frames (4*w*h bytes each). Candidates are -maxShift to
// maxShift; each is scored by the mean absolute difference of the luminance
// over the columns both frames show, and the lowest wins (a tie goes to the
// smallest |s|). An error is returned for frames of the wrong size or a
// maxShift that leaves no overlap.
func Shift(a, b []byte, w, h, maxShift int) (int, error) {
	if w < 1 || h < 1 || len(a) != 4*w*h || len(b) != 4*w*h {
		return 0, fmt.Errorf("framediff: frames of %d and %d bytes are not %dx%d RGBA", len(a), len(b), w, h)
	}
	if maxShift < 0 || maxShift >= w {
		return 0, fmt.Errorf("framediff: max shift %d leaves no overlap in %d columns", maxShift, w)
	}
	la, lb := luma(a), luma(b)
	best, bestScore := 0, -1.0
	for _, s := range candidates(maxShift) {
		var sum float64
		n := 0
		for x := max(s, 0); x < min(w, w+s); x++ {
			for y := 0; y < h; y++ {
				d := lb[y*w+x] - la[y*w+x-s]
				if d < 0 {
					d = -d
				}
				sum += d
				n++
			}
		}
		score := sum / float64(n)
		if bestScore < 0 || score < bestScore {
			best, bestScore = s, score
		}
	}
	return best, nil
}

// candidates are 0, -1, 1, -2, 2 ...: by increasing |s|, so a tie keeps the
// smallest.
func candidates(m int) []int {
	out := []int{0}
	for i := 1; i <= m; i++ {
		out = append(out, -i, i)
	}
	return out
}

// luma is the Rec. 601 luminance of each pixel of an RGBA frame.
func luma(p []byte) []float64 {
	out := make([]float64, len(p)/4)
	for i := range out {
		out[i] = 0.299*float64(p[4*i]) + 0.587*float64(p[4*i+1]) + 0.114*float64(p[4*i+2])
	}
	return out
}
