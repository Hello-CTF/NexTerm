package update

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// errPayloadMissingExecutable lets callers name the archive that lacked the executable.
var errPayloadMissingExecutable = errors.New("更新包中没有 nexterm-desktop")

// installLinuxPayload stages the executable and its optional native runtime
// together. An interrupted extraction cannot publish a half-written library.
func installLinuxPayload(ctx context.Context, reader *tar.Reader, executable string) error {
	directory := filepath.Dir(executable)
	temporary, err := os.MkdirTemp(directory, ".nexterm-update-")
	if err != nil {
		return err
	}
	retainBackup := false
	defer func() {
		if !retainBackup {
			_ = os.RemoveAll(temporary)
		}
	}()
	staged := filepath.Join(temporary, "nexterm-desktop")
	runtimeStage := filepath.Join(temporary, "ai-runtime")
	found := false
	hasRuntime := false
	seen := map[string]bool{}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(header.Name, "./")
		runtimeFile := strings.HasPrefix(name, "usr/bin/ai-runtime/")
		if name != "usr/bin/nexterm-desktop" && !runtimeFile {
			continue
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return errors.New("更新包包含不支持的运行时文件类型")
		}
		if path.Clean(name) != name || strings.Contains(name, "\\") || header.Size < 0 || header.Size > 1<<30 {
			return errors.New("更新包文件路径或大小无效")
		}
		total += header.Size
		if total > 2<<30 {
			return errors.New("更新包解压大小超出限制")
		}
		if seen[name] {
			return errors.New("更新包包含重复文件")
		}
		seen[name] = true
		destination := staged
		mode := os.FileMode(0755)
		if runtimeFile {
			relative := strings.TrimPrefix(name, "usr/bin/ai-runtime/")
			if !filepath.IsLocal(filepath.FromSlash(relative)) {
				return errors.New("更新包运行时路径无效")
			}
			destination = filepath.Join(runtimeStage, filepath.FromSlash(relative))
			mode = 0644
			hasRuntime = true
			if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
				return err
			}
		}
		output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, reader)
		syncErr := output.Sync()
		closeErr := output.Close()
		if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
			return err
		}
		if !runtimeFile {
			found = true
		}
	}
	if !found {
		return errPayloadMissingExecutable
	}
	if hasRuntime && !runtimeUnavailable(runtimeStage) {
		const name = "libonnxruntime.so"
		if info, err := os.Stat(filepath.Join(runtimeStage, name)); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("更新包缺少 AI 运行时 %s", name)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runtimeTarget := filepath.Join(directory, "ai-runtime")
	backup := filepath.Join(temporary, "old-runtime")
	hadRuntime := false
	if hasRuntime {
		if _, err := os.Lstat(runtimeTarget); err == nil {
			if err := os.Rename(runtimeTarget, backup); err != nil {
				return err
			}
			hadRuntime = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(runtimeStage, runtimeTarget); err != nil {
			if hadRuntime {
				restoreErr := os.Rename(backup, runtimeTarget)
				retainBackup = restoreErr != nil
				err = errors.Join(err, restoreErr)
			}
			return err
		}
	}
	if err := os.Rename(staged, executable); err != nil {
		if hasRuntime {
			err = errors.Join(err, os.RemoveAll(runtimeTarget))
			if hadRuntime {
				restoreErr := os.Rename(backup, runtimeTarget)
				retainBackup = restoreErr != nil
				err = errors.Join(err, restoreErr)
			}
		}
		return err
	}
	return nil
}

// runtimeUnavailable reports whether runtime.json in directory declares that no
// ONNX Runtime library exists for this target. Such a payload ships only that
// manifest, so the library must not be required.
func runtimeUnavailable(directory string) bool {
	data, err := os.ReadFile(filepath.Join(directory, "runtime.json"))
	if err != nil {
		return false
	}
	var manifest struct {
		Unavailable string `json:"unavailable"`
	}
	return json.Unmarshal(data, &manifest) == nil && manifest.Unavailable != ""
}
