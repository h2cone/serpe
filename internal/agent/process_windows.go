//go:build windows

package agent

import (
	"os"
	"os/exec"
	"strconv"
)

func configureCommandProcessGroup(cmd *exec.Cmd) {}

func terminateCommandProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}
