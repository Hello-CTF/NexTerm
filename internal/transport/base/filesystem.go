package base

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"time"
)

type FileKind string

const (
	FileFile      FileKind = "file"
	FileDirectory FileKind = "dir"
	FileSymlink   FileKind = "symlink"
	FileOther     FileKind = "other"
)

type FileEntry struct {
	Name          string      `json:"name"`
	Path          string      `json:"path"`
	Kind          FileKind    `json:"kind"`
	Size          int64       `json:"size"`
	Mode          fs.FileMode `json:"mode"`
	Owner         string      `json:"owner,omitempty"`
	Group         string      `json:"group,omitempty"`
	ModTime       time.Time   `json:"modTime"`
	SymlinkTarget string      `json:"symlinkTarget,omitempty"`
}

type PathStyle string

const (
	PathStyleUnknown PathStyle = "unknown"
	PathStyleWindows PathStyle = "windows"
	PathStylePOSIX   PathStyle = "posix"
)

var ErrNoMedia = errors.New("filesystem media not ready")

type FileRoot struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

type FileSystemRoots struct {
	PathStyle PathStyle  `json:"pathStyle"`
	Roots     []FileRoot `json:"roots"`
	HomePath  string     `json:"homePath"`
	Supported bool       `json:"supported"`
}

type RootsProvider interface {
	Roots(context.Context) (FileSystemRoots, error)
}

type RemoteReader interface {
	io.ReadCloser
	Size() int64
}

type RemoteWriter interface {
	io.WriteCloser
	Sync() error
}

type FileSystem interface {
	List(context.Context, string) ([]FileEntry, error)
	ReadFile(context.Context, string, int64) ([]byte, error)
	WriteFile(context.Context, string, []byte, bool) error
	Mkdir(context.Context, string) error
	Rename(context.Context, string, string) error
	Delete(context.Context, string, bool) error
	Chmod(context.Context, string, fs.FileMode) error
	Checksum(context.Context, string, string) (string, error)
	Exists(context.Context, string) (bool, error)
	Size(context.Context, string) (int64, error)
	OpenRead(context.Context, string) (RemoteReader, error)
	OpenWrite(context.Context, string, bool) (RemoteWriter, error)
}
