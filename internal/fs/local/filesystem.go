package local

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const BackupSuffix = ".nexterm-bak"

type FileSystem struct{}

func New() *FileSystem {
	return &FileSystem{}
}

func (f *FileSystem) LocalPath(path string) string {
	return real(path)
}

func real(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	rest := strings.TrimLeft(path[1:], `/\`)
	if rest == "" {
		return home
	}
	return filepath.Join(home, rest)
}

func (f *FileSystem) List(ctx context.Context, path string) ([]base.FileEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := real(path)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", root, err)
	}
	result := make([]base.FileEntry, 0, len(entries))
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		item := base.FileEntry{Name: entry.Name(), Path: path, Kind: base.FileOther}
		info, err := os.Lstat(path)
		if err != nil {
			result = append(result, item)
			continue
		}
		item.Kind = fileKind(info.Mode())
		item.Size = info.Size()
		item.Mode = info.Mode()
		item.ModTime = info.ModTime()
		item.Owner, item.Group = ownerGroup(info)
		if info.Mode()&os.ModeSymlink != 0 {
			item.SymlinkTarget, _ = os.Readlink(path)
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if (result[i].Kind == base.FileDirectory) != (result[j].Kind == base.FileDirectory) {
			return result[i].Kind == base.FileDirectory
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func fileKind(mode fs.FileMode) base.FileKind {
	switch {
	case mode&fs.ModeSymlink != 0:
		return base.FileSymlink
	case mode.IsDir():
		return base.FileDirectory
	case mode.IsRegular():
		return base.FileFile
	default:
		return base.FileOther
	}
}

func (f *FileSystem) ReadFile(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("read limit must not be negative")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(real(path))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("file exceeds read limit (%d > %d bytes)", info.Size(), maxBytes)
	}
	var data []byte
	if maxBytes == int64(^uint64(0)>>1) {
		data, err = io.ReadAll(file)
	} else {
		data, err = io.ReadAll(io.LimitReader(file, maxBytes+1))
	}
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file exceeds read limit (%d > %d bytes)", len(data), maxBytes)
	}
	return data, nil
}

func (f *FileSystem) Mkdir(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.MkdirAll(real(path), 0o755)
}

func (f *FileSystem) Rename(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(real(from), real(to))
}

func (f *FileSystem) Delete(ctx context.Context, path string, isDir bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if isDir {
		return os.RemoveAll(real(path))
	}
	return os.Remove(real(path))
}

func (f *FileSystem) Checksum(ctx context.Context, path, algorithm string) (string, error) {
	var digest hash.Hash
	switch strings.ToLower(strings.TrimSpace(algorithm)) {
	case "md5":
		digest = md5.New()
	case "sha256", "sha-256":
		digest = sha256.New()
	default:
		return "", fmt.Errorf("unsupported checksum algorithm %q (want md5 or sha256)", algorithm)
	}
	file, err := os.Open(real(path))
	if err != nil {
		return "", err
	}
	defer file.Close()
	buffer := make([]byte, 1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			if _, err := digest.Write(buffer[:n]); err != nil {
				return "", err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (f *FileSystem) Exists(ctx context.Context, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, err := os.Stat(real(path))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (f *FileSystem) Size(ctx context.Context, path string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	info, err := os.Stat(real(path))
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

var _ base.FileSystem = (*FileSystem)(nil)
