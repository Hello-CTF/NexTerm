//go:build linux

package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// applyInstall installs the executable and bundled privacy runtime from the signed deb.
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
		return userErrorf(UserErrorIO, "解压 %s 失败: %w", filepath.Base(archive), err)
	}
	defer compressed.Close()
	if err := installLinuxPayload(ctx, tar.NewReader(compressed), m.ExePath); err != nil {
		return userErrorf(UserErrorIO, "安装更新文件失败: %w", err)
	}
	m.restartTarget = m.ExePath
	return nil
}

// debDataTarGz locates the data.tar.gz member in the deb (ar container) and returns its content stream;
// it only parses the short-name GNU ar members produced by this repo's packaging, and errors on any other layout.
func debDataTarGz(file *os.File) (io.Reader, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	header := make([]byte, 8)
	if _, err := io.ReadFull(file, header); err != nil || string(header) != "!<arch>\n" {
		return nil, userErrorf(UserErrorIO, "不是合法的 deb 包")
	}
	for {
		member := make([]byte, 60)
		if _, err := io.ReadFull(file, member); err == io.EOF {
			break
		} else if err != nil {
			return nil, userErrorf(UserErrorIO, "读取 deb 成员头失败: %w", err)
		}
		if string(member[58:60]) != "`\n" {
			return nil, userErrorf(UserErrorIO, "deb 成员头损坏")
		}
		name := strings.TrimRight(string(member[0:16]), " ")
		size, err := strconv.Atoi(strings.TrimSpace(string(member[48:58])))
		if err != nil || size < 0 {
			return nil, userErrorf(UserErrorIO, "deb 成员大小非法: %q", string(member[48:58]))
		}
		if name == "data.tar.gz/" {
			return io.LimitReader(file, int64(size)), nil
		}
		if _, err := file.Seek(int64(size)+int64(size%2), io.SeekCurrent); err != nil {
			return nil, err
		}
	}
	return nil, userErrorf(UserErrorIO, "deb 包中没有 data.tar.gz")
}
