//go:build !wasip1

package main

import (
	"fmt"
	"os"
)

// main is the native stub: the guest is main.go (wasip1), and this file only lets
// `go build ./...` and `go test` build the package on the host, where tone.go's
// pacing and generator are tested.
func main() {
	fmt.Fprintln(os.Stderr, "tone is a wasip1 guest: GOOS=wasip1 GOARCH=wasm go build ./images/tone")
	os.Exit(2)
}
