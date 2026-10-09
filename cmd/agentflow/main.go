package main

import (
	"os"

	"github.com/chenhg5/agentflow/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		cli.PrintError(err)
		os.Exit(cli.ExitCode(err))
	}
}
