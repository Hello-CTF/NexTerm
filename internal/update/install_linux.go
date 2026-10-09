//go:build linux

package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// applyInstall 从 tar.gz 解出 nexterm-desktop, 原子替换当前可执行文件
// (rename 覆盖运行中的 exe 在 Linux 上安全)。
func (m *Manager) applyInstall(ctx context.Context, archive string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("解压 %s 失败: %w", filepath.Base(archive), err)
	}
	defer compressed.Close()
	staged := m.ExePath + ".new"
	found := false
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", filepath.Base(archive), err)
		}
		if header.Typeflag != tar.TypeReg || !strings.HasSuffix(header.Name, "/nexterm-desktop") {
			continue
		}
		output, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, reader)
		closeErr := output.Close()
		if copyErr != nil {
			_ = os.Remove(staged)
			return fmt.Errorf("写出新可执行文件失败: %w", copyErr)
		}
		if closeErr != nil {
			_ = os.Remove(staged)
			return closeErr
		}
		found = true
		break
	}
	if !found {
		return fmt.Errorf("%s 中没有 nexterm-desktop", filepath.Base(archive))
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(staged)
		return err
	}
	if err := os.Rename(staged, m.ExePath); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("替换当前可执行文件失败: %w", err)
	}
	m.restartTarget = m.ExePath
	return nil
}
