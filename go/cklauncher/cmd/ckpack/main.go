package main

import (
	"fmt"
	"os"

	"github.com/GecEnterprises/corekiosk/go/ckpack"
)

func main() {
	if err := ckpack.RunCLI(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "\nckpack error: %v\n", err)
		os.Exit(1)
	}
}
