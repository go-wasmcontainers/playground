package framediff

import (
	"math/rand"
	"testing"
)

// scene is a w x h frame of noise (every column differs), so a shift has one
// best match.
func scene(w, h int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	p := make([]byte, 4*w*h)
	for i := 0; i < w*h; i++ {
		p[4*i], p[4*i+1], p[4*i+2], p[4*i+3] = byte(r.Intn(256)), byte(r.Intn(256)), byte(r.Intn(256)), 255
	}
	return p
}

// moved is a with every row moved s pixels to the right (to the left for s < 0);
// the columns that enter are black.
func moved(a []byte, w, h, s int) []byte {
	b := make([]byte, len(a))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if sx := x - s; sx >= 0 && sx < w {
				copy(b[4*(y*w+x):4*(y*w+x)+4], a[4*(y*w+sx):4*(y*w+sx)+4])
			}
		}
	}
	return b
}

func TestShift(t *testing.T) {
	const w, h = 120, 16
	a := scene(w, h, 1)
	for _, s := range []int{-30, -7, -1, 0, 1, 9, 30} {
		got, err := Shift(a, moved(a, w, h, s), w, h, 40)
		if err != nil || got != s {
			t.Errorf("a frame moved by %d: Shift = %d, %v", s, got, err)
		}
	}
}

func TestShiftSameFrame(t *testing.T) {
	a := scene(60, 8, 2)
	if got, err := Shift(a, a, 60, 8, 20); err != nil || got != 0 {
		t.Errorf("the same frame: %d, %v", got, err)
	}
}

func TestShiftErrors(t *testing.T) {
	a := scene(10, 4, 3)
	for _, c := range []struct {
		name string
		a, b []byte
		w, h int
		max  int
	}{
		{"short frame", a[:8], a, 10, 4, 3},
		{"other size", a, scene(10, 5, 3), 10, 4, 3},
		{"zero width", nil, nil, 0, 4, 0},
		{"no overlap", a, a, 10, 4, 10},
		{"negative max", a, a, 10, 4, -1},
	} {
		if _, err := Shift(c.a, c.b, c.w, c.h, c.max); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

func TestMeanAbsDiff(t *testing.T) {
	if d, err := MeanAbsDiff([]byte{0, 10, 20}, []byte{0, 10, 20}); err != nil || d != 0 {
		t.Errorf("same: %v, %v", d, err)
	}
	if d, err := MeanAbsDiff([]byte{0, 10, 20}, []byte{3, 10, 17}); err != nil || d != 2 {
		t.Errorf("(3+0+3)/3: %v, %v", d, err)
	}
	for _, c := range [][2][]byte{{nil, nil}, {{1}, {1, 2}}} {
		if _, err := MeanAbsDiff(c[0], c[1]); err == nil {
			t.Errorf("accepted %v", c)
		}
	}
}
