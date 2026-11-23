//go:build linux

package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ulikunitz/xz"
)

// dpkgInfoDir holds the file lists of installed Debian packages; a var for tests.
var dpkgInfoDir = "/var/lib/dpkg/info"

// installBlocker reports why the running copy cannot be replaced in place, before anything is downloaded: a copy
// installed by the deb package belongs to the package manager, and a directory this user cannot write (such as
// /usr/bin) cannot take the new files.
func (m *Manager) installBlocker() string {
	if m.ExePath == "" {
		return ""
	}
	if debPackageOwns(dpkgInfoDir, m.ExePath) {
		return "NexTerm 由系统软件包（deb）安装，应用内更新会绕过软件包管理器；请下载新版 .deb 安装包，用系统软件包管理器安装"
	}
	directory := filepath.Dir(m.ExePath)
	if err := directoryWritable(directory); err != nil {
		return fmt.Sprintf("安装目录 %s 不可写，无法在应用内更新；请下载新版 .deb 安装包，用系统软件包管理器安装", directory)
	}
	return ""
}

// debPackageOwns reports whether the nexterm deb's file list names the executable.
func debPackageOwns(infoDir, executable string) bool {
	data, err := os.ReadFile(filepath.Join(infoDir, "nexterm.list"))
	if err != nil {
		return false
	}
	candidates := []string{filepath.Clean(executable)}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		candidates = append(candidates, resolved)
	}
	for _, line := range strings.Split(string(data), "\n") {
		for _, candidate := range candidates {
			if strings.TrimSpace(line) == candidate {
				return true
			}
		}
	}
	return false
}

// applyInstall installs the executable and bundled privacy runtime from the signed deb, read through the verified
// handle so the file that was hashed is the file that is extracted.
func (m *Manager) applyInstall(ctx context.Context, archive string, verified *os.File) (string, error) {
	if _, err := verified.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if err := extractDebPayload(ctx, verified, filepath.Base(archive), m.ExePath); err != nil {
		return "", err
	}
	return m.ExePath, nil
}

// checkPackage extracts the deb into a scratch directory exactly as applyInstall would, so a package that cannot
// be installed is reported when it is staged rather than when the user restarts. The scratch directory sits next
// to the download in the stage directory, not in a temp directory that may be a small tmpfs.
func (m *Manager) checkPackage(ctx context.Context, archive string) error {
	scratch, err := os.MkdirTemp(filepath.Dir(archive), "check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	return extractDebPayload(ctx, file, filepath.Base(archive), filepath.Join(scratch, "nexterm-desktop"))
}

func extractDebPayload(ctx context.Context, file *os.File, name, executable string) error {
	source, member, err := debDataTar(file)
	if err != nil {
		return err
	}
	var payload io.Reader
	switch member {
	case "data.tar.xz":
		reader, err := xz.NewReader(source)
		if err != nil {
			return userErrorf(UserErrorIO, "解压 %s 失败: %w", name, err)
		}
		payload = reader
	default:
		reader, err := gzip.NewReader(source)
		if err != nil {
			return userErrorf(UserErrorIO, "解压 %s 失败: %w", name, err)
		}
		defer reader.Close()
		payload = reader
	}
	if err := installLinuxPayload(ctx, tar.NewReader(payload), executable); err != nil {
		if errors.Is(err, errPayloadMissingExecutable) {
			return userErrorf(UserErrorIO, "%s 中没有 nexterm-desktop", name)
		}
		return userErrorf(UserErrorIO, "安装更新文件失败: %w", err)
	}
	return nil
}

// debDataTar locates the data.tar.xz or data.tar.gz member in the deb (ar container) and returns its content
// stream and member name;
// it only parses the short-name GNU ar members produced by this repo's packaging, and errors on any other layout.
func debDataTar(file *os.File) (io.Reader, string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	header := make([]byte, 8)
	if _, err := io.ReadFull(file, header); err != nil || string(header) != "!<arch>\n" {
		return nil, "", userErrorf(UserErrorIO, "不是合法的 deb 包")
	}
	for {
		member := make([]byte, 60)
		if _, err := io.ReadFull(file, member); err == io.EOF {
			break
		} else if err != nil {
			return nil, "", userErrorf(UserErrorIO, "读取 deb 成员头失败: %w", err)
		}
		if string(member[58:60]) != "`\n" {
			return nil, "", userErrorf(UserErrorIO, "deb 成员头损坏")
		}
		name := strings.TrimRight(string(member[0:16]), " ")
		size, err := strconv.Atoi(strings.TrimSpace(string(member[48:58])))
		if err != nil || size < 0 {
			return nil, "", userErrorf(UserErrorIO, "deb 成员大小非法: %q", string(member[48:58]))
		}
		if name == "data.tar.xz/" || name == "data.tar.gz/" {
			return io.LimitReader(file, int64(size)), strings.TrimSuffix(name, "/"), nil
		}
		if _, err := file.Seek(int64(size)+int64(size%2), io.SeekCurrent); err != nil {
			return nil, "", err
		}
	}
	return nil, "", userErrorf(UserErrorIO, "deb 包中没有 data.tar.xz 或 data.tar.gz")
}
