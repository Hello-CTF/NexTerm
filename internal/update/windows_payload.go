package update

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const windowsBackupPrefix = ".nexterm-update-old-"

// installWindowsPayload moves loaded images out of the installer's destination.
// Windows refuses to rename a directory that contains a mapped image, so the
// runtime files move one by one while their directory stays in place.
// Successful backups remain until the next install sweeps them, because the
// running process may still hold the old executable and ONNX Runtime open.
func installWindowsPayload(executable string, install func() error) error {
	directory := filepath.Dir(executable)
	sweepWindowsBackups(directory)
	backup, err := os.MkdirTemp(directory, windowsBackupPrefix)
	if err != nil {
		return err
	}
	runtimeTarget := filepath.Join(directory, "ai-runtime")
	runtimeBackup := filepath.Join(backup, "ai-runtime")
	oldExecutable := filepath.Join(backup, filepath.Base(executable))
	if err := os.Rename(executable, oldExecutable); err != nil {
		_ = os.Remove(backup)
		return err
	}
	moved, err := moveTreeFiles(runtimeTarget, runtimeBackup)
	if err != nil {
		restoreErr := restoreFiles(runtimeBackup, runtimeTarget, moved)
		return errors.Join(err, restoreErr, os.Rename(oldExecutable, executable))
	}
	rollback := func(cause error) error {
		executableRemove := os.RemoveAll(executable)
		executableRestore := os.Rename(oldExecutable, executable)
		runtimeRemove := os.RemoveAll(runtimeTarget)
		var runtimeRestore error
		if len(moved) > 0 {
			runtimeRestore = restoreFiles(runtimeBackup, runtimeTarget, moved)
		}
		err := errors.Join(cause, executableRemove, executableRestore, runtimeRemove, runtimeRestore)
		if executableRestore == nil && runtimeRestore == nil {
			_ = os.RemoveAll(backup)
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
	// Best effort only: loaded Windows images cannot be deleted until they are
	// unmapped; sweepWindowsBackups removes them on a later install.
	_ = os.RemoveAll(backup)
	return nil
}

// sweepWindowsBackups removes backups left by earlier updates. Entries still
// mapped by a running process fail to delete and are retried next time.
func sweepWindowsBackups(directory string) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), windowsBackupPrefix) {
			_ = os.RemoveAll(filepath.Join(directory, entry.Name()))
		}
	}
}

// moveTreeFiles renames every regular file below source into destination and
// returns the relative paths moved so far, including on failure.
func moveTreeFiles(source, destination string) ([]string, error) {
	var moved []string
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if path == source && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("AI 运行时包含不支持的文件类型 %s", path)
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.Rename(path, target); err != nil {
			return err
		}
		moved = append(moved, relative)
		return nil
	})
	return moved, err
}

func restoreFiles(source, destination string, relatives []string) error {
	var errs []error
	for _, relative := range relatives {
		target := filepath.Join(destination, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, os.Rename(filepath.Join(source, relative), target))
	}
	return errors.Join(errs...)
}
