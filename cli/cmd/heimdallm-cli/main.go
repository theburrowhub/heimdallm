package main

import (
	"io"
	"os"

	"github.com/theburrowhub/heimdallm/cli/internal/cli"
)

var version = "dev"
var exitProcess = os.Exit

func run(args []string, stdout, stderr io.Writer) error {
	cmd := cli.NewRootCmd(version)
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd.Execute()
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		exitProcess(1)
	}
}
