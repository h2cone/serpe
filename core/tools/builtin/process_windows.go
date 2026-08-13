//go:build windows

package builtin

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

type processController struct {
	command *exec.Cmd
	job     windows.Handle
	kill    sync.Once
}

func validateProcessContainment() error {
	if err := ntResumeProcess.Find(); err != nil {
		return fmt.Errorf("builtin: suspended Bash launch is unavailable: %w", err)
	}
	job, err := newKillOnCloseJob()
	if err != nil {
		return fmt.Errorf("builtin: Windows Job Object containment is unavailable: %w", err)
	}
	return windows.CloseHandle(job)
}

func newProcessController(command *exec.Cmd) (*processController, error) {
	if command == nil {
		return nil, fmt.Errorf("builtin: nil Bash command")
	}
	job, err := newKillOnCloseJob()
	if err != nil {
		return nil, fmt.Errorf("builtin: create Bash Job Object: %w", err)
	}
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED,
	}
	return &processController{command: command, job: job}, nil
}

func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	// BREAKAWAY_OK and SILENT_BREAKAWAY_OK are deliberately absent. The
	// process must remain in this job unless the operating system rejects the
	// assignment, in which case start fails before Bash is resumed.
	return job, nil
}

func (p *processController) start() error {
	if err := p.command.Start(); err != nil {
		_ = windows.CloseHandle(p.job)
		p.job = 0
		return err
	}
	var containmentErr error
	if err := p.command.Process.WithHandle(func(raw uintptr) {
		handle := windows.Handle(raw)
		if err := windows.AssignProcessToJobObject(p.job, handle); err != nil {
			containmentErr = fmt.Errorf("assign Bash to Job Object: %w", err)
			return
		}
		status, _, _ := ntResumeProcess.Call(uintptr(handle))
		if status != 0 {
			containmentErr = fmt.Errorf("resume suspended Bash process: NTSTATUS 0x%08x", uint32(status))
		}
	}); err != nil && containmentErr == nil {
		containmentErr = fmt.Errorf("access suspended Bash process: %w", err)
	}
	if containmentErr == nil {
		return nil
	}

	// Bash is still suspended if assignment or resume failed. Closing the job
	// kills an assigned process; Process.Kill covers an assignment failure.
	p.terminate()
	_ = p.command.Process.Kill()
	_, _ = p.command.Process.Wait()
	return containmentErr
}

func (p *processController) terminate() {
	if p == nil {
		return
	}
	p.kill.Do(func() {
		if p.job != 0 {
			_ = windows.CloseHandle(p.job)
			p.job = 0
		}
	})
}
