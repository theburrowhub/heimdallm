package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "auto-pr daemon starting...")
	os.Exit(0)
}
