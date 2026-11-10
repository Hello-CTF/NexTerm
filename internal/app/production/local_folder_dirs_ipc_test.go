package production

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func authorizeLocalDir(t *testing.T, dispatcher *ipc.Dispatcher, root string) localFolderAuthorizeDTO {
	t.Helper()
	response := dispatcher.Dispatch(context.Background(), ipc.Request{
		Command: "local_folder_authorize",
		Args:    json.RawMessage(`{"path":` + localPathJSON(root) + `}`),
	}, ipc.Environment{})
	if response.Error != nil {
		t.Fatalf("authorize %s: %+v", root, response.Error)
	}
	raw, _ := json.Marshal(response.Data)
	var grant localFolderAuthorizeDTO
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	return grant
}

func dispatchLocalDirsToken[T any](t *testing.T, dispatcher *ipc.Dispatcher, command, token string, paths []string) (T, *ipc.Error) {
	t.Helper()
	var result T
	args, err := json.Marshal(localDirsRequest{Token: token, Paths: paths})
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

// dispatchLocalDirs authorizes the first path's folder (or its given root) and runs the command
// against paths below it. Tests pass the root first.
func dispatchLocalDirs[T any](t *testing.T, dispatcher *ipc.Dispatcher, command string, paths []string) (T, *ipc.Error) {
	t.Helper()
	grant := authorizeLocalDir(t, dispatcher, paths[0])
	return dispatchLocalDirsToken[T](t, dispatcher, command, grant.Token, paths)
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
	if _, ipcErr := dispatchLocalDirsToken[localDirsListDTO](t, dispatcher, "local_folder_entries", authorizeLocalDir(t, dispatcher, root).Token, []string{"relative"}); ipcErr == nil || ipcErr.Code != ipc.CodeBadParam {
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
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	dispatcher := newLocalFolderDispatcher(t)
	grant := authorizeLocalDir(t, dispatcher, root)
	paths := []string{grant.Root, filepath.Join(grant.Root, "linkdir"), filepath.Join(grant.Root, "linkdir", "sub")}
	result, ipcErr := dispatchLocalDirsToken[localDirsListDTO](t, dispatcher, "local_folder_entries", grant.Token, paths)
	if ipcErr != nil || len(result.Dirs) != 3 {
		t.Fatalf("list = %+v, %+v", result, ipcErr)
	}
	kinds := map[string]string{}
	for _, entry := range result.Dirs[0].Entries {
		kinds[entry.Name] = entry.Kind
	}
	if kinds["link.txt"] != "symlink" || kinds["linkdir"] != "symlink" {
		t.Fatalf("entries = %+v", result.Dirs[0].Entries)
	}
	if result.Dirs[1].Kind != "symlink" || len(result.Dirs[1].Entries) != 0 || result.Dirs[2].Kind != "symlink" {
		t.Fatalf("symlinked folders = %+v", result.Dirs[1:])
	}
}

func TestLocalDirsCreateDoesNotTraverseSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs elevated rights on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	dispatcher := newLocalFolderDispatcher(t)
	grant := authorizeLocalDir(t, dispatcher, root)
	result, ipcErr := dispatchLocalDirsToken[localDirsCreateDTO](t, dispatcher, "local_folder_create", grant.Token,
		[]string{filepath.Join(grant.Root, "linkdir", "made"), filepath.Join(grant.Root, "ok", "deep")})
	if ipcErr != nil || len(result.Failed) != 1 || result.Failed[0].Path != filepath.Join(grant.Root, "linkdir", "made") {
		t.Fatalf("create = %+v, %+v", result, ipcErr)
	}
	if _, err := os.Stat(filepath.Join(outside, "made")); !os.IsNotExist(err) {
		t.Fatalf("created through a symlink: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, "ok", "deep")); err != nil || !info.IsDir() {
		t.Fatalf("regular folder = %v", err)
	}
}

func TestLocalDirsRejectPathsOutsideGrantedFolder(t *testing.T) {
	dispatcher := newLocalFolderDispatcher(t)
	root := t.TempDir()
	other := t.TempDir()
	grant := authorizeLocalDir(t, dispatcher, root)
	for _, command := range []string{"local_folder_entries", "local_folder_create"} {
		for _, path := range []string{other, filepath.Join(grant.Root, "..", filepath.Base(other), "x"), filepath.Dir(grant.Root)} {
			var ipcErr *ipc.Error
			if command == "local_folder_entries" {
				_, ipcErr = dispatchLocalDirsToken[localDirsListDTO](t, dispatcher, command, grant.Token, []string{path})
			} else {
				_, ipcErr = dispatchLocalDirsToken[localDirsCreateDTO](t, dispatcher, command, grant.Token, []string{path})
			}
			if ipcErr == nil || ipcErr.Code != ipc.CodeBadParam {
				t.Fatalf("%s %s = %+v", command, path, ipcErr)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(other, "x")); err == nil {
		t.Fatal("folder created outside the grant")
	}
	for _, token := range []string{"", "nope"} {
		if _, ipcErr := dispatchLocalDirsToken[localDirsListDTO](t, dispatcher, "local_folder_entries", token, []string{root}); ipcErr == nil || ipcErr.Code != ipc.CodeBadParam {
			t.Fatalf("token %q = %+v", token, ipcErr)
		}
	}
}

func TestLocalFolderGrantExpiresAndIsBounded(t *testing.T) {
	now := time.Unix(1000, 0)
	grants := newLocalFolderGrants(func() time.Time { return now })
	dir := t.TempDir()
	first, err := grants.authorize(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grants.resolve(first.Token); err != nil {
		t.Fatal(err)
	}
	now = now.Add(localFolderGrantTTL + time.Second)
	if _, err := grants.resolve(first.Token); err == nil {
		t.Fatal("expired grant resolved")
	}
	for i := 0; i < localFolderGrantMax+5; i++ {
		now = now.Add(time.Second)
		if _, err := grants.authorize(dir); err != nil {
			t.Fatal(err)
		}
	}
	if len(grants.grants) > localFolderGrantMax {
		t.Fatalf("grants = %d", len(grants.grants))
	}
	if _, err := grants.authorize(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing folder authorized")
	}
	if _, err := grants.authorize(filepath.VolumeName(dir) + string(filepath.Separator)); err == nil {
		t.Fatal("filesystem root authorized")
	}
}

func TestLocalDirsCreateMakesParentsAndReportsFailures(t *testing.T) {
	dispatcher := newLocalFolderDispatcher(t)
	root := t.TempDir()
	writeLocalFile(t, filepath.Join(root, "file"), "x")
	nested := filepath.Join(root, "a", "b", "c")
	blocked := filepath.Join(root, "file", "child")
	result, ipcErr := dispatchLocalDirsToken[localDirsCreateDTO](t, dispatcher, "local_folder_create", authorizeLocalDir(t, dispatcher, root).Token, []string{nested, blocked, nested})
	if ipcErr != nil {
		t.Fatal(ipcErr)
	}
	if info, err := os.Stat(nested); err != nil || !info.IsDir() {
		t.Fatalf("nested folder = %v", err)
	}
	if len(result.Failed) != 1 || result.Failed[0].Path != blocked || result.Failed[0].Error == "" {
		t.Fatalf("failures = %+v", result.Failed)
	}
	if _, ipcErr := dispatchLocalDirsToken[localDirsCreateDTO](t, dispatcher, "local_folder_create", authorizeLocalDir(t, dispatcher, root).Token, []string{""}); ipcErr == nil || ipcErr.Code != ipc.CodeBadParam {
		t.Fatalf("empty path = %+v", ipcErr)
	}
}
