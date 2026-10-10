package main

import (
	"fmt"
	"os"

	"github.com/simp-lee/oxpio/internal/cli"
)

func main() {
	if err := cli.Execute(os.Args[1:]); err != nil {
		if silent, ok := err.(interface{ Silent() bool }); !ok || !silent.Silent() {
			_, _ = fmt.Fprintln(os.Stderr, err)
		}
		exitCode := 1
		if coded, ok := err.(interface{ ExitCode() int }); ok && coded.ExitCode() > 0 {
			exitCode = coded.ExitCode()
		}
		os.Exit(exitCode)
	}
}
