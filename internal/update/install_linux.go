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
	"strconv"
	"strings"
)

// applyInstall 从 deb (data.tar.gz 内的 usr/bin/nexterm-desktop) 解出
// nexterm-desktop, 原子替换当前可执行文件 (rename 覆盖运行中的 exe 在 Linux 上安全)。
func (m *Manager) applyInstall(ctx context.Context, archive string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	source, err := debDataTarGz(file)
	if err != nil {
		return err
	}
	compressed, err := gzip.NewReader(source)
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
			return fmt.Errorf("写入新可执行文件失败：%w", copyErr)
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

// debDataTarGz 定位 deb (ar 容器) 里的 data.tar.gz 成员并返回其内容流;
// 只解析本仓库打包产出的短名 GNU ar 成员, 其余布局直接报错。
func debDataTarGz(file *os.File) (io.Reader, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	header := make([]byte, 8)
	if _, err := io.ReadFull(file, header); err != nil || string(header) != "!<arch>\n" {
		return nil, fmt.Errorf("不是合法的 deb 包")
	}
	for {
		member := make([]byte, 60)
		if _, err := io.ReadFull(file, member); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("读取 deb 成员头失败: %w", err)
		}
		if string(member[58:60]) != "`\n" {
			return nil, fmt.Errorf("deb 成员头损坏")
		}
		name := strings.TrimRight(string(member[0:16]), " ")
		size, err := strconv.Atoi(strings.TrimSpace(string(member[48:58])))
		if err != nil || size < 0 {
			return nil, fmt.Errorf("deb 成员大小非法: %q", string(member[48:58]))
		}
		if name == "data.tar.gz/" {
			return io.LimitReader(file, int64(size)), nil
		}
		if _, err := file.Seek(int64(size)+int64(size%2), io.SeekCurrent); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("deb 包中没有 data.tar.gz")
}
