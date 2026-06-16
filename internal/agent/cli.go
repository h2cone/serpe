package agent

import (
	"fmt"
	"strings"
)

// CLIArgs holds the positional arguments accepted by the example agent.
type CLIArgs struct {
	Task  string
	Model string
}

// ParseCLI parses command-line arguments of the form: <model> <task...>.
func ParseCLI(args []string) (CLIArgs, error) {
	switch {
	case len(args) == 0:
		return CLIArgs{}, fmt.Errorf("missing required positional argument: model")
	case len(args) == 1:
		return CLIArgs{}, fmt.Errorf("missing required positional argument: task")
	}

	return CLIArgs{
		Model: args[0],
		Task:  strings.Join(args[1:], " "),
	}, nil
}
