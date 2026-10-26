//go:build !js

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "engine runs in a browser worker: build it with GOOS=js GOARCH=wasm")
	os.Exit(2)
}
