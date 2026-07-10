package main

import (
	"fmt"
	"strings"
)

type cliArgs struct {
	Task  string
	Model string
}

func parseCLI(args []string) (cliArgs, error) {
	switch {
	case len(args) == 0:
		return cliArgs{}, fmt.Errorf("missing required positional argument: model")
	case len(args) == 1:
		return cliArgs{}, fmt.Errorf("missing required positional argument: task")
	}

	return cliArgs{
		Model: args[0],
		Task:  strings.Join(args[1:], " "),
	}, nil
}
