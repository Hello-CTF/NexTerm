//go:build windows

package mount

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"syscall"

	"github.com/UserExistsError/conpty"
	"golang.org/x/sys/windows"
)

func configureCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000,
	}
}

func runInteractive(ctx context.Context, input command) (commandResult, error) {
	if err := ctx.Err(); err != nil {
		return commandResult{}, err
	}
	arguments := append([]string{input.name}, input.args...)
	terminal, err := conpty.Start(windows.ComposeCommandLine(arguments))
	if err != nil {
		return commandResult{}, err
	}
	processHandle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(terminal.Pid()))
	if err != nil {
		_ = terminal.Close()
		return commandResult{}, err
	}
	var terminateOnce sync.Once
	terminate := func() {
		terminateOnce.Do(func() { _ = windows.TerminateProcess(processHandle, 1) })
	}
	cancellationDone := make(chan struct{})
	stopCancellation := context.AfterFunc(ctx, func() {
		terminate()
		close(cancellationDone)
	})
	defer func() {
		if !stopCancellation() {
			<-cancellationDone
		}
		_ = windows.CloseHandle(processHandle)
	}()

	var output bytes.Buffer
	outputDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&output, terminal)
		close(outputDone)
	}()
	var writeErr error
	if ctx.Err() != nil {
		terminate()
	} else {
		_, writeErr = io.Copy(terminal, bytes.NewReader(input.stdin))
		if writeErr != nil {
			terminate()
		}
	}
	exitCode, waitErr := terminal.Wait(context.Background())
	if waitErr != nil {
		terminate()
	}
	closeErr := terminal.Close()
	<-outputDone
	result := commandResult{stderr: output.String()}
	if writeErr != nil {
		return result, commandContextError(ctx, errors.Join(writeErr, waitErr, closeErr))
	}
	if waitErr != nil {
		return result, commandContextError(ctx, errors.Join(waitErr, closeErr))
	}
	if ctx.Err() != nil && exitCode != 0 {
		return result, ctx.Err()
	}
	if exitCode != 0 {
		return result, &commandExitError{code: int(exitCode)}
	}
	return result, nil
}
