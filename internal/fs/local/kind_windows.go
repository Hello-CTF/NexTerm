//go:build windows

package local

import (
	"io/fs"
	"syscall"
	"unsafe"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"golang.org/x/sys/windows"
)

// reparseNameSurrogate marks reparse tags that redirect to another named file (symlinks, junctions and similar).
const reparseNameSurrogate = 0x20000000

type fileAttributeTagInfo struct {
	FileAttributes uint32
	ReparseTag     uint32
}

func win32Attributes(info fs.FileInfo) (uint32, bool) {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || data == nil {
		return 0, false
	}
	return data.FileAttributes, true
}

// platformFileKind classifies reparse points that Go reports as irregular: a junction behaves as a link to a
// directory, while non-surrogate reparse points (cloud placeholders, app execution aliases) are ordinary files
// or directories for browsing purposes.
func platformFileKind(path string, info fs.FileInfo) base.FileKind {
	attributes, ok := win32Attributes(info)
	if !ok || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		return fileKind(info.Mode())
	}
	tag, err := reparseTag(path)
	if err != nil {
		return fileKind(info.Mode())
	}
	switch {
	case tag == windows.IO_REPARSE_TAG_SYMLINK, tag == windows.IO_REPARSE_TAG_MOUNT_POINT:
		return base.FileSymlink
	case tag&reparseNameSurrogate != 0:
		return fileKind(info.Mode())
	case attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0:
		return base.FileDirectory
	default:
		return base.FileFile
	}
}

func platformHidden(_ string, info fs.FileInfo) bool {
	attributes, ok := win32Attributes(info)
	return ok && attributes&(windows.FILE_ATTRIBUTE_HIDDEN|windows.FILE_ATTRIBUTE_SYSTEM) != 0
}

// reparseTag reads the tag without following the reparse point, so a cloud placeholder is not hydrated.
func reparseTag(path string) (uint32, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(handle)
	var info fileAttributeTagInfo
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return 0, err
	}
	return info.ReparseTag, nil
}
