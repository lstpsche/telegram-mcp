package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func executeEntry(ctx context.Context, executable string, environment []string, input io.ReadCloser, output io.WriteCloser, diagnostics io.Writer) (code int, result error) {
	stdin, inOK := input.(*os.File)
	stdout, outOK := output.(*os.File)
	stderr, errOK := diagnostics.(*os.File)
	if !inOK || !outOK || !errOK {
		return 1, errors.New("managed relay requires inherited standard handles")
	}
	if err := ctx.Err(); err != nil {
		return 1, err
	}
	job, err := relayProcessJob()
	if err != nil {
		return 1, err
	}
	// The short deadline is passed through the environment. The process uses
	// the lifetime context so a connected relay is not killed after startup.
	command := exec.CommandContext(ctx, executable)
	command.Env = environment
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	command.WaitDelay = time.Second
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	defer func() {
		if command.Process != nil && (command.ProcessState == nil || !command.ProcessState.Exited()) {
			// Keep the only job handle open until this one-shot wrapper exits.
			// Closing it here would terminate the wrapper before it reports failure.
			return
		}
		if err := relayJobLimit(job, 0); err != nil {
			// An uncertain guard must remain armed until process exit.
			result = errors.Join(result, err)
			return
		}
		result = errors.Join(result, windows.CloseHandle(job))
	}()
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 && ctx.Err() == nil {
			return exit.ExitCode(), nil
		}
		return 1, errors.Join(ctx.Err(), err)
	}
	return 0, nil
}

func relayProcessJob() (windows.Handle, error) {
	// The handle is not inheritable. Assigning the wrapper before CreateProcess
	// makes child membership atomic even if the wrapper is killed during startup.
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	if err := relayJobLimit(job, windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE); err != nil {
		return 0, errors.Join(err, windows.CloseHandle(job))
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		return 0, errors.Join(err, windows.CloseHandle(job))
	}
	return job, nil
}

func relayJobLimit(job windows.Handle, flags uint32) error {
	var information windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	information.BasicLimitInformation.LimitFlags = flags
	var pinned runtime.Pinner
	pinned.Pin(&information)
	defer pinned.Unpin()
	_, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&information)), uint32(unsafe.Sizeof(information)))
	return err
}
