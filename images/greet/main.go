// greet says hello to $NAME (default "world"): try `wctr run -e NAME=you greet`.
package main

import (
	"fmt"
	"os"
)

func main() {
	name := os.Getenv("NAME")
	if name == "" {
		name = "world"
	}
	fmt.Printf("Hello, %s!\n", name)
}
