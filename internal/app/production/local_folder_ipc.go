package production

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const localFileReadDefaultMaxBytes = 256 << 20

type localFolderListRequest struct {
	Path string `json:"path"`
}

type localFolderFileDTO struct {
	RelativePath string `json:"relativePath"`
	LocalPath    string `json:"localPath"`
	Size         int64  `json:"size"`
	MtimeMs      int64  `json:"mtimeMs"`
}

type localFolderListDTO struct {
	Name      string               `json:"name"`
	Files     []localFolderFileDTO `json:"files"`
	EmptyDirs []string             `json:"emptyDirs"`
}

type localFileReadRequest struct {
	Path     string `json:"path"`
	MaxBytes int64  `json:"maxBytes"`
}

// registerLocalFolderCommands serves the desktop folder-upload flow: the webview cannot expose OS
// paths of picked files, so the native folder dialog returns the picked folder path and these
// commands walk/read it locally. Registered only in the desktop assembly; on the server they would
// turn /rpc into an arbitrary server-filesystem listing/read channel.
func registerLocalFolderCommands(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "local_folder_list", func(_ context.Context, _ *ipc.Call, input localFolderListRequest) (localFolderListDTO, error) {
				return listLocalFolder(input.Path)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "local_file_read", func(_ context.Context, _ *ipc.Call, input localFileReadRequest) (fsReadDTO, error) {
				return readLocalFile(input.Path, input.MaxBytes)
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

// listLocalFolder walks a user-picked folder for upload: RelativePath always uses forward slashes
// (remote join paths and tar entries are POSIX), LocalPath keeps native separators for local reads.
// Symlinks and other non-regular entries are skipped and never followed, so nothing outside the
// picked folder is read; a symlink passed as the root itself is followed because the user picked it.
func listLocalFolder(root string) (localFolderListDTO, error) {
	info, err := os.Stat(root)
	if err != nil {
		return localFolderListDTO{}, localFolderError(err)
	}
	if !info.IsDir() {
		return localFolderListDTO{}, ipc.BadParam(fmt.Errorf("%s 不是文件夹", root))
	}
	name := filepath.Base(filepath.Clean(root))
	if name == "" || name == "/" || name == `\` || name == "." {
		return localFolderListDTO{}, ipc.BadParam(fmt.Errorf("不能上传磁盘根目录 %s，请选择其中的文件夹", root))
	}
	result := localFolderListDTO{
		Name:      name,
		Files:     []localFolderFileDTO{},
		EmptyDirs: []string{},
	}
	if err := walkLocalFolder(root, "", &result); err != nil {
		return localFolderListDTO{}, localFolderError(err)
	}
	return result, nil
}

func walkLocalFolder(dir, prefix string, result *localFolderListDTO) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) == 0 && prefix != "" {
		result.EmptyDirs = append(result.EmptyDirs, prefix)
	}
	for _, entry := range entries {
		rel := entry.Name()
		if prefix != "" {
			rel = prefix + "/" + entry.Name()
		}
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			if err := walkLocalFolder(path, rel, result); err != nil {
				return err
			}
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		result.Files = append(result.Files, localFolderFileDTO{
			RelativePath: rel,
			LocalPath:    path,
			Size:         info.Size(),
			MtimeMs:      info.ModTime().UnixMilli(),
		})
	}
	return nil
}

func readLocalFile(path string, maxBytes int64) (fsReadDTO, error) {
	if maxBytes <= 0 {
		maxBytes = localFileReadDefaultMaxBytes
	}
	info, err := os.Stat(path)
	if err != nil {
		return fsReadDTO{}, localFolderError(err)
	}
	if !info.Mode().IsRegular() {
		return fsReadDTO{}, ipc.BadParam(fmt.Errorf("%s 不是常规文件", path))
	}
	if info.Size() > maxBytes {
		return fsReadDTO{}, ipc.BadParam(fmt.Errorf("文件 %s 的大小 %d 字节超过读取上限 %d 字节", path, info.Size(), maxBytes))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fsReadDTO{}, localFolderError(err)
	}
	return fsReadDTO{Path: path, Size: len(data), ContentBase64: base64.StdEncoding.EncodeToString(data)}, nil
}

func localFolderError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ipc.WrapError(ipc.CodeNotFound, err.Error(), err)
	}
	return err
}
