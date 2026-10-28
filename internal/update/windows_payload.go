package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// installWindowsPayload moves loaded images out of the installer's destination.
// Successful backups remain until the process exits because Windows may still
// hold the old executable and inference DLLs open.
func installWindowsPayload(executable string, install func() error) error {
	directory := filepath.Dir(executable)
	backup, err := os.MkdirTemp(directory, ".nexterm-update-old-")
	if err != nil {
		return err
	}
	runtimeTarget := filepath.Join(directory, "ai-runtime")
	oldExecutable := filepath.Join(backup, filepath.Base(executable))
	oldRuntime := filepath.Join(backup, "ai-runtime")
	if err := os.Rename(executable, oldExecutable); err != nil {
		_ = os.Remove(backup)
		return err
	}
	hadRuntime := false
	if _, err := os.Lstat(runtimeTarget); err == nil {
		if err := os.Rename(runtimeTarget, oldRuntime); err != nil {
			return errors.Join(err, os.Rename(oldExecutable, executable))
		}
		hadRuntime = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.Join(err, os.Rename(oldExecutable, executable))
	}
	rollback := func(cause error) error {
		executableRemove := os.RemoveAll(executable)
		executableRestore := os.Rename(oldExecutable, executable)
		runtimeRemove := os.RemoveAll(runtimeTarget)
		var runtimeRestore error
		if hadRuntime {
			runtimeRestore = os.Rename(oldRuntime, runtimeTarget)
		}
		err := errors.Join(cause, executableRemove, executableRestore, runtimeRemove, runtimeRestore)
		if executableRestore == nil && runtimeRestore == nil {
			_ = os.Remove(backup)
		}
		return err
	}
	if err := install(); err != nil {
		return rollback(err)
	}
	for _, name := range []string{executable, filepath.Join(runtimeTarget, "onnxruntime.dll")} {
		info, err := os.Lstat(name)
		if err != nil {
			return rollback(fmt.Errorf("安装完成后文件不可用 %s: %w", name, err))
		}
		if !info.Mode().IsRegular() {
			return rollback(fmt.Errorf("安装完成后文件类型无效 %s", name))
		}
	}
	// Best effort only: loaded Windows images cannot be deleted until restart.
	_ = os.RemoveAll(backup)
	return nil
}
