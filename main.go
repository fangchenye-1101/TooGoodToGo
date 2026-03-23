package main

import (
	"fmt"
	"os"

	"github.com/fangchen/tgtg-auto/workflow"
)

func main() {
	if err := workflow.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n[ERROR] %v\n", err)
		os.Exit(1)
	}
}
