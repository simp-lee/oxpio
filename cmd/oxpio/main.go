package main

import (
	"fmt"
	"os"

	"github.com/simp-lee/oxpio/internal/cli"
)

func main() {
	if err := cli.Execute(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
