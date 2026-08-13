//go:build !windows

package builtin

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type processController struct {
	command *exec.Cmd
	kill    sync.Once
}

func validateProcessContainment() error { return nil }

func newProcessController(command *exec.Cmd) (*processController, error) {
	if command == nil {
		return nil, fmt.Errorf("builtin: nil Bash command")
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &processController{command: command}, nil
}

func (p *processController) start() error { return p.command.Start() }

func (p *processController) terminate() {
	if p == nil {
		return
	}
	p.kill.Do(func() {
		if p.command == nil || p.command.Process == nil {
			return
		}
		pid := p.command.Process.Pid
		if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return
			}
			_ = p.command.Process.Kill()
			return
		}
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		<-timer.C
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	})
}
