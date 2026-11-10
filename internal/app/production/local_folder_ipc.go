package production

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/atomicfile"
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

type localFileWriteRequest struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"contentBase64"`
}

// localFileWriteDTO reports Exists instead of writing when the target is already there, so a save
// never replaces a local file the user did not choose to replace.
type localFileWriteDTO struct {
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
	Exists bool   `json:"exists"`
}

type localFileReadRangeRequest struct {
	Path     string `json:"path"`
	Offset   int64  `json:"offset"`
	MaxBytes int64  `json:"maxBytes"`
}

type localDirsRequest struct {
	// Token is the grant local_folder_authorize issued for the picked folder; every path must be inside it.
	Token string   `json:"token"`
	Paths []string `json:"paths"`
}

type localFolderAuthorizeRequest struct {
	Path string `json:"path"`
}

// localFolderAuthorizeDTO carries the grant for a picked folder. Root is the symlink-resolved folder
// the grant covers; the caller builds target paths below it.
type localFolderAuthorizeDTO struct {
	Token string `json:"token"`
	Root  string `json:"root"`
}

// localDirEntryDTO is one entry of a local download target folder. Kind is "file", "dir",
// "symlink" or "other"; a symlink is reported as such and never followed.
type localDirEntryDTO struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Size    int64  `json:"size"`
	MtimeMs int64  `json:"mtimeMs"`
}

type localDirListingDTO struct {
	Path string `json:"path"`
	// Kind is "missing" when nothing is at Path, "dir" for a readable folder, or the kind of the
	// non-folder entry occupying Path.
	Kind    string             `json:"kind"`
	Entries []localDirEntryDTO `json:"entries"`
}

type localDirsListDTO struct {
	Dirs []localDirListingDTO `json:"dirs"`
}

type localDirCreateFailureDTO struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

type localDirsCreateDTO struct {
	Failed []localDirCreateFailureDTO `json:"failed"`
}

// localDirsMaxPaths bounds one local_folder_entries/local_folder_create request.
const localDirsMaxPaths = 20000

// registerLocalFolderCommands serves the desktop folder-upload flow: the webview cannot expose OS
// paths of picked files, so the native folder dialog returns the picked folder path and these
// commands walk/read it locally. The same desktop-only set prepares folder downloads and saves files and images a remote
// desktop copied. Registered only in the desktop assembly; on the server they would
// turn /rpc into an arbitrary server-filesystem listing/read channel.
func registerLocalFolderCommands(dispatcher *ipc.Dispatcher) error {
	grants := newLocalFolderGrants(time.Now)
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "local_folder_authorize", func(_ context.Context, _ *ipc.Call, input localFolderAuthorizeRequest) (localFolderAuthorizeDTO, error) {
				return grants.authorize(input.Path)
			})
		},
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
		func() error {
			return ipc.Register(dispatcher, "local_file_write", func(_ context.Context, _ *ipc.Call, input localFileWriteRequest) (localFileWriteDTO, error) {
				return saveLocalFile(input.Path, input.ContentBase64)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "local_folder_entries", func(ctx context.Context, _ *ipc.Call, input localDirsRequest) (localDirsListDTO, error) {
				root, err := grants.resolve(input.Token)
				if err != nil {
					return localDirsListDTO{}, err
				}
				return listLocalDirs(ctx, root, input.Paths)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "local_folder_create", func(ctx context.Context, _ *ipc.Call, input localDirsRequest) (localDirsCreateDTO, error) {
				root, err := grants.resolve(input.Token)
				if err != nil {
					return localDirsCreateDTO{}, err
				}
				return createLocalDirs(ctx, root, input.Paths)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "clipboard_write_image", func(ctx context.Context, _ *ipc.Call, input clipboardImageWriteRequest) (clipboardImageWriteDTO, error) {
				return writeClipboardImage(ctx, input.PNGBase64)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "local_file_read_range", func(_ context.Context, _ *ipc.Call, input localFileReadRangeRequest) (fsRangeReadDTO, error) {
				return readLocalFileRange(input.Path, input.Offset, input.MaxBytes)
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

// saveLocalFile saves bytes received in the webview (a file copied on a remote desktop) under a
// path the user picked. Missing parent folders are created; an existing entry is never replaced.
func saveLocalFile(path, contentBase64 string) (localFileWriteDTO, error) {
	if path == "" || !filepath.IsAbs(path) {
		return localFileWriteDTO{}, ipc.BadParam(fmt.Errorf("保存路径必须是绝对路径"))
	}
	data, err := base64.StdEncoding.DecodeString(contentBase64)
	if err != nil {
		return localFileWriteDTO{}, ipc.BadParam(fmt.Errorf("文件内容不是有效的 Base64: %w", err))
	}
	if int64(len(data)) > localFileReadDefaultMaxBytes {
		return localFileWriteDTO{}, ipc.BadParam(fmt.Errorf("文件大小 %d 字节超过写入上限 %d 字节", len(data), localFileReadDefaultMaxBytes))
	}
	if _, err := os.Lstat(path); err == nil {
		return localFileWriteDTO{Path: path, Exists: true}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return localFileWriteDTO{}, localFolderError(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return localFileWriteDTO{}, localFolderError(err)
	}
	if err := atomicfile.Write(path, 0o600, func(writer io.Writer) error {
		_, err := writer.Write(data)
		return err
	}); err != nil {
		return localFileWriteDTO{}, localFolderError(err)
	}
	return localFileWriteDTO{Path: path, Bytes: len(data)}, nil
}

func readLocalFileRange(path string, offset, maxBytes int64) (fsRangeReadDTO, error) {
	if offset < 0 {
		return fsRangeReadDTO{}, ipc.BadParam(fmt.Errorf("offset 不能为负数"))
	}
	if maxBytes == 0 {
		maxBytes = 1 << 20
	}
	if maxBytes < 0 || maxBytes > fsRangeReadMaxBytes {
		return fsRangeReadDTO{}, ipc.BadParam(fmt.Errorf("maxBytes 必须在 1 到 %d 字节之间", fsRangeReadMaxBytes))
	}
	file, err := os.Open(path)
	if err != nil {
		return fsRangeReadDTO{}, localFolderError(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fsRangeReadDTO{}, localFolderError(err)
	}
	if !info.Mode().IsRegular() {
		return fsRangeReadDTO{}, ipc.BadParam(fmt.Errorf("%s 不是常规文件", path))
	}
	if offset > info.Size() {
		return fsRangeReadDTO{}, ipc.BadParam(fmt.Errorf("offset %d 超过文件大小 %d", offset, info.Size()))
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return fsRangeReadDTO{}, localFolderError(err)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes))
	if err != nil {
		return fsRangeReadDTO{}, localFolderError(err)
	}
	return fsRangeReadDTO{
		Path: path, Offset: offset, Size: info.Size(), ContentBase64: base64.StdEncoding.EncodeToString(data),
		Truncated: offset+int64(len(data)) < info.Size(),
	}, nil
}

// Grants live for the folder-download flow only: a short-lived token per picked folder, bounded in
// number, so create/entries cannot be pointed at paths the user never picked. The native dialog runs
// in the webview, so the backend learns the choice from the authorize call that follows the pick; the
// token still confines every later request to that one folder.
const (
	localFolderGrantTTL = time.Hour
	localFolderGrantMax = 32
)

type localFolderGrant struct {
	root    string
	expires time.Time
}

type localFolderGrants struct {
	mu     sync.Mutex
	now    func() time.Time
	grants map[string]localFolderGrant
}

func newLocalFolderGrants(now func() time.Time) *localFolderGrants {
	return &localFolderGrants{now: now, grants: map[string]localFolderGrant{}}
}

func (g *localFolderGrants) authorize(path string) (localFolderAuthorizeDTO, error) {
	if path == "" || !filepath.IsAbs(path) {
		return localFolderAuthorizeDTO{}, ipc.BadParam(fmt.Errorf("本地目录路径必须是绝对路径: %q", path))
	}
	root, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return localFolderAuthorizeDTO{}, localFolderError(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return localFolderAuthorizeDTO{}, localFolderError(err)
	}
	if !info.IsDir() {
		return localFolderAuthorizeDTO{}, ipc.BadParam(fmt.Errorf("%s 不是文件夹", path))
	}
	if filepath.Dir(root) == root {
		return localFolderAuthorizeDTO{}, ipc.BadParam(fmt.Errorf("不能把磁盘根目录 %s 作为下载目标，请选择其中的文件夹", path))
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return localFolderAuthorizeDTO{}, err
	}
	token := hex.EncodeToString(raw[:])
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for key, grant := range g.grants {
		if !grant.expires.After(now) {
			delete(g.grants, key)
		}
	}
	for len(g.grants) >= localFolderGrantMax {
		oldest, oldestKey := time.Time{}, ""
		for key, grant := range g.grants {
			if oldestKey == "" || grant.expires.Before(oldest) {
				oldest, oldestKey = grant.expires, key
			}
		}
		delete(g.grants, oldestKey)
	}
	g.grants[token] = localFolderGrant{root: root, expires: now.Add(localFolderGrantTTL)}
	return localFolderAuthorizeDTO{Token: token, Root: root}, nil
}

func (g *localFolderGrants) resolve(token string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	grant, ok := g.grants[token]
	if !ok || !grant.expires.After(g.now()) {
		delete(g.grants, token)
		return "", ipc.BadParam(fmt.Errorf("下载目标文件夹授权已失效，请重新选择文件夹"))
	}
	return grant.root, nil
}

// localDirRelative returns the segments of path below root. Paths must be absolute and, once cleaned,
// lie inside root; the root itself yields no segments.
func localDirRelative(root, path string) ([]string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, ipc.BadParam(fmt.Errorf("本地目录路径必须是绝对路径: %q", path))
	}
	rel, err := filepath.Rel(root, filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, ipc.BadParam(fmt.Errorf("路径 %s 不在所选下载文件夹内", path))
	}
	if rel == "." {
		return nil, nil
	}
	return strings.Split(rel, string(filepath.Separator)), nil
}

func validateLocalDirPaths(root string, paths []string) ([][]string, error) {
	if len(paths) > localDirsMaxPaths {
		return nil, ipc.BadParam(fmt.Errorf("一次最多处理 %d 个本地目录", localDirsMaxPaths))
	}
	result := make([][]string, 0, len(paths))
	for _, path := range paths {
		segments, err := localDirRelative(root, path)
		if err != nil {
			return nil, err
		}
		result = append(result, segments)
	}
	return result, nil
}

// errLocalSymlink marks a path that crosses a symlink below the granted root; downloads never write
// through links.
var errLocalSymlink = errors.New("路径包含符号链接，不会被跟随")

// lstatBelow Lstats root/segments one component at a time and stops at the first symlink, so a link
// inside the granted folder is never traversed. A missing component or a file in the middle reports
// as fs.ErrNotExist/ENOTDIR from the failing component.
func lstatBelow(root string, segments []string) (fs.FileInfo, error) {
	current := root
	var info fs.FileInfo
	for i := 0; i <= len(segments); i++ {
		if i > 0 {
			current = filepath.Join(current, segments[i-1])
		}
		var err error
		info, err = os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if i < len(segments) {
			if info.Mode()&fs.ModeSymlink != 0 {
				return info, errLocalSymlink
			}
			if !info.IsDir() {
				return nil, syscall.ENOTDIR
			}
		}
	}
	return info, nil
}

// mkdirBelow creates root/segments without traversing symlinks: every existing component must be a
// real folder.
func mkdirBelow(root string, segments []string) error {
	current := root
	for _, segment := range segments {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		switch {
		case err == nil:
			if info.Mode()&fs.ModeSymlink != 0 {
				return fmt.Errorf("%s: %w", current, errLocalSymlink)
			}
			if !info.IsDir() {
				return &fs.PathError{Op: "mkdir", Path: current, Err: syscall.ENOTDIR}
			}
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

func localEntryKind(mode fs.FileMode) string {
	switch {
	case mode.IsDir():
		return "dir"
	case mode&fs.ModeSymlink != 0:
		return "symlink"
	case mode.IsRegular():
		return "file"
	default:
		return "other"
	}
}

// listLocalDirs reports the entries of local folders a download writes into, so the frontend can
// detect conflicts and pick free names before queueing. A missing folder is not an error; a folder
// that is a symlink, or sits below one, is reported as "symlink" and never followed.
func listLocalDirs(ctx context.Context, root string, paths []string) (localDirsListDTO, error) {
	relatives, err := validateLocalDirPaths(root, paths)
	if err != nil {
		return localDirsListDTO{}, err
	}
	result := localDirsListDTO{Dirs: make([]localDirListingDTO, 0, len(paths))}
	for index, path := range paths {
		if err := ctx.Err(); err != nil {
			return localDirsListDTO{}, err
		}
		listing := localDirListingDTO{Path: path, Kind: "missing", Entries: []localDirEntryDTO{}}
		info, err := lstatBelow(root, relatives[index])
		switch {
		case errors.Is(err, errLocalSymlink):
			listing.Kind = "symlink"
			result.Dirs = append(result.Dirs, listing)
			continue
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
			// ENOTDIR: a parent is a file, so nothing can be at this path either.
			result.Dirs = append(result.Dirs, listing)
			continue
		case err != nil:
			return localDirsListDTO{}, localFolderError(err)
		}
		listing.Kind = localEntryKind(info.Mode())
		if listing.Kind == "dir" {
			entries, err := os.ReadDir(filepath.Join(append([]string{root}, relatives[index]...)...))
			if err != nil {
				return localDirsListDTO{}, localFolderError(err)
			}
			for _, entry := range entries {
				entryInfo, err := entry.Info()
				if err != nil {
					if errors.Is(err, fs.ErrNotExist) {
						continue
					}
					return localDirsListDTO{}, localFolderError(err)
				}
				listing.Entries = append(listing.Entries, localDirEntryDTO{
					Name: entry.Name(), Kind: localEntryKind(entryInfo.Mode()), Size: entryInfo.Size(), MtimeMs: entryInfo.ModTime().UnixMilli(),
				})
			}
		}
		result.Dirs = append(result.Dirs, listing)
	}
	return result, nil
}

// createLocalDirs creates download target folders with their parents inside the granted root, never
// through a symlink. Each failure is reported per path so the files below it can be marked failed
// while the rest of the batch proceeds.
func createLocalDirs(ctx context.Context, root string, paths []string) (localDirsCreateDTO, error) {
	relatives, err := validateLocalDirPaths(root, paths)
	if err != nil {
		return localDirsCreateDTO{}, err
	}
	result := localDirsCreateDTO{Failed: []localDirCreateFailureDTO{}}
	for index, path := range paths {
		if err := ctx.Err(); err != nil {
			return localDirsCreateDTO{}, err
		}
		if err := mkdirBelow(root, relatives[index]); err != nil {
			result.Failed = append(result.Failed, localDirCreateFailureDTO{Path: path, Error: err.Error()})
		}
	}
	return result, nil
}

func localFolderError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ipc.WrapError(ipc.CodeNotFound, err.Error(), err)
	}
	return err
}
