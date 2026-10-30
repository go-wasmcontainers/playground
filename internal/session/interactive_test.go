package session

import "testing"

// TestInteractive: a line gets the terminal's input exactly when wctr's
// own flags say -i, wherever they stand among the others. (Only exec has
// -i in the engine today; run's lines follow when it does, with no change
// here, since the flags are wctr's own.)
func TestInteractive(t *testing.T) {
	s := &Session{}
	for line, want := range map[string]bool{
		"wctr exec -it c sh":                    true,
		"wctr exec -ti c sh":                    true,
		"wctr exec -i c sh":                     true,
		"wctr exec -e A=1 --interactive c sh":   true,
		"wctr exec --interactive=false c sh":    false,
		"wctr exec -t c sh":                     false,
		"wctr exec c sh -i":                     false, // the command's -i, not wctr's
		"wctr -H inproc://engine exec -it c sh": true,
		"wctr run --rm -d shell":                false,
		"wctr ps -a":                            false,
		"help":                                  false,
		"wctr run 'open":                        false,
		"wctr":                                  false,
		"docker exec -it c sh":                  false,
	} {
		if got := s.Interactive(line); got != want {
			t.Errorf("%q: %v, want %v", line, got, want)
		}
	}
}
