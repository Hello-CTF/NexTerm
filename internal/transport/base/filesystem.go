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
	// TargetKind is what a symlink resolves to (dir, file or other); it stays empty for non-links and broken links.
	TargetKind FileKind `json:"targetKind,omitempty"`
	Hidden     bool     `json:"hidden,omitempty"`
}

// TargetKindOf classifies the followed metadata of a symlink target.
func TargetKindOf(mode fs.FileMode) FileKind {
	switch {
	case mode.IsDir():
		return FileDirectory
	case mode.IsRegular():
		return FileFile
	default:
		return FileOther
	}
}

// DotHidden reports the POSIX convention that a leading dot hides an entry.
func DotHidden(name string) bool {
	return len(name) > 1 && name[0] == '.' && name != ".."
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
	PathStyle    PathStyle        `json:"pathStyle"`
	Roots        []FileRoot       `json:"roots"`
	HomePath     string           `json:"homePath"`
	Supported    bool             `json:"supported"`
	Capabilities FileCapabilities `json:"capabilities"`
}

// ChmodSupport describes how far a filesystem honours permission changes.
type ChmodSupport string

const (
	// ChmodPOSIX applies full POSIX permission bits.
	ChmodPOSIX ChmodSupport = "posix"
	// ChmodReadOnly maps the owner write bit to the Windows read-only attribute.
	ChmodReadOnly ChmodSupport = "readonly"
	ChmodNone     ChmodSupport = "none"
)

// FileCapabilities lets the file manager offer only the operations a backend can perform.
type FileCapabilities struct {
	Chmod           ChmodSupport `json:"chmod"`
	Pack            bool         `json:"pack"`
	Extract         bool         `json:"extract"`
	CaseInsensitive bool         `json:"caseInsensitive"`
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

// ErrNotPrivateDir reports that a freshly created private directory did not verify as a real owner-only directory.
var ErrNotPrivateDir = errors.New("directory is not a private directory")

// PrivateDirCreator creates one directory exclusively with owner-only (0700) permissions. It fails when the path
// already exists in any form, including a symlink, never creates parents, and verifies the result is a real directory.
type PrivateDirCreator interface {
	MkdirPrivate(context.Context, string) error
}
