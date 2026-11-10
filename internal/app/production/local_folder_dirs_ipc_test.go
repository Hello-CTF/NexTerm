package production

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func dispatchLocalDirs[T any](t *testing.T, dispatcher *ipc.Dispatcher, command string, paths []string) (T, *ipc.Error) {
	t.Helper()
	var result T
	args, err := json.Marshal(localDirsRequest{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: command, Args: args}, ipc.Environment{})
	if response.Error != nil {
		return result, response.Error
	}
	raw, err := json.Marshal(response.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result, nil
}

func TestLocalDirsListReportsEntriesAndMissingTargets(t *testing.T) {
	dispatcher := newLocalFolderDispatcher(t)
	root := t.TempDir()
	writeLocalFile(t, filepath.Join(root, "a.txt"), "abc")
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths := []string{root, filepath.Join(root, "missing"), filepath.Join(root, "a.txt"), filepath.Join(root, "a.txt", "below")}
	result, ipcErr := dispatchLocalDirs[localDirsListDTO](t, dispatcher, "local_folder_entries", paths)
	if ipcErr != nil || len(result.Dirs) != 4 {
		t.Fatalf("list = %+v, %+v", result, ipcErr)
	}
	byName := map[string]localDirEntryDTO{}
	for _, entry := range result.Dirs[0].Entries {
		byName[entry.Name] = entry
	}
	if result.Dirs[0].Kind != "dir" || byName["a.txt"].Kind != "file" || byName["a.txt"].Size != 3 || byName["a.txt"].MtimeMs == 0 || byName["sub"].Kind != "dir" {
		t.Fatalf("root listing = %+v", result.Dirs[0])
	}
	if result.Dirs[1].Kind != "missing" || len(result.Dirs[1].Entries) != 0 {
		t.Fatalf("missing listing = %+v", result.Dirs[1])
	}
	if result.Dirs[2].Kind != "file" {
		t.Fatalf("a file where a folder is needed = %+v", result.Dirs[2])
	}
	if result.Dirs[3].Kind != "missing" {
		t.Fatalf("a path below a file = %+v", result.Dirs[3])
	}
	if _, ipcErr := dispatchLocalDirs[localDirsListDTO](t, dispatcher, "local_folder_entries", []string{"relative"}); ipcErr == nil || ipcErr.Code != ipc.CodeBadParam {
		t.Fatalf("relative path = %+v", ipcErr)
	}
}

func TestLocalDirsListReportsSymlinkEntriesWithoutFollowing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs elevated rights on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	writeLocalFile(t, filepath.Join(outside, "secret.txt"), "x")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	result, ipcErr := dispatchLocalDirs[localDirsListDTO](t, newLocalFolderDispatcher(t), "local_folder_entries", []string{root})
	if ipcErr != nil || len(result.Dirs) != 1 || len(result.Dirs[0].Entries) != 1 || result.Dirs[0].Entries[0].Kind != "symlink" {
		t.Fatalf("list = %+v, %+v", result, ipcErr)
	}
}

func TestLocalDirsCreateMakesParentsAndReportsFailures(t *testing.T) {
	dispatcher := newLocalFolderDispatcher(t)
	root := t.TempDir()
	writeLocalFile(t, filepath.Join(root, "file"), "x")
	nested := filepath.Join(root, "a", "b", "c")
	blocked := filepath.Join(root, "file", "child")
	result, ipcErr := dispatchLocalDirs[localDirsCreateDTO](t, dispatcher, "local_folder_create", []string{nested, blocked, nested})
	if ipcErr != nil {
		t.Fatal(ipcErr)
	}
	if info, err := os.Stat(nested); err != nil || !info.IsDir() {
		t.Fatalf("nested folder = %v", err)
	}
	if len(result.Failed) != 1 || result.Failed[0].Path != blocked || result.Failed[0].Error == "" {
		t.Fatalf("failures = %+v", result.Failed)
	}
	if _, ipcErr := dispatchLocalDirs[localDirsCreateDTO](t, dispatcher, "local_folder_create", []string{""}); ipcErr == nil || ipcErr.Code != ipc.CodeBadParam {
		t.Fatalf("empty path = %+v", ipcErr)
	}
}
