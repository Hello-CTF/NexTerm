package ssh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fs/chunked"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/pkg/sftp"
)

func TestSFTPFileOperationsBackupAndChecksum(t *testing.T) {
	filesystem, _ := newTestFS(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := filesystem.Mkdir(ctx, "dir"); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFile(ctx, "dir/file.txt", []byte("old"), false); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Chmod(ctx, "dir/file.txt", 0o640); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFile(ctx, "dir/file.txt", []byte("new"), true); err != nil {
		t.Fatal(err)
	}
	backup, err := filesystem.ReadFile(ctx, "dir/file.txt.nexterm-bak", 100)
	if err != nil || string(backup) != "old" {
		t.Fatalf("backup = %q, %v", backup, err)
	}
	data, err := filesystem.ReadFile(ctx, "dir/file.txt", 100)
	if err != nil || string(data) != "new" {
		t.Fatalf("file = %q, %v", data, err)
	}
	if _, err := filesystem.ReadFile(ctx, "dir/file.txt", 2); err == nil {
		t.Fatal("read limit was not enforced")
	}
	digest := sha256.Sum256([]byte("new"))
	checksum, err := filesystem.Checksum(ctx, "dir/file.txt", "sha256")
	if err != nil || checksum != hex.EncodeToString(digest[:]) {
		t.Fatalf("checksum = %q, %v", checksum, err)
	}
	if _, err := filesystem.Checksum(ctx, "dir/file.txt", "sha512"); err == nil {
		t.Fatal("unsupported checksum algorithm succeeded")
	}
	if size, err := filesystem.Size(ctx, "dir/file.txt"); err != nil || size != 3 {
		t.Fatalf("size = %d, %v", size, err)
	}
	if exists, err := filesystem.Exists(ctx, "dir/file.txt"); err != nil || !exists {
		t.Fatalf("exists = %v, %v", exists, err)
	}
	entries, err := filesystem.List(ctx, "dir")
	if err != nil || len(entries) != 2 {
		t.Fatalf("list = %+v, %v", entries, err)
	}
	writer, err := filesystem.OpenWrite(ctx, "dir/file.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("+")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, _ = filesystem.ReadFile(ctx, "dir/file.txt", 100)
	if string(data) != "new+" {
		t.Fatalf("append = %q", data)
	}
	if err := filesystem.Rename(ctx, "dir/file.txt", "dir/renamed.txt"); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Delete(ctx, "dir/file.txt.nexterm-bak", false); err != nil {
		t.Fatal(err)
	}
	if exists, err := filesystem.Exists(ctx, "dir/file.txt.nexterm-bak"); err != nil || exists {
		t.Fatalf("deleted backup exists = %v, %v", exists, err)
	}
	if err := filesystem.Delete(ctx, "dir/renamed.txt", false); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Delete(ctx, "dir", true); err != nil {
		t.Fatal(err)
	}
}

func TestSFTPDeleteSymlinkToDirectoryRemovesLink(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	ctx := context.Background()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "kept.txt"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Delete(ctx, "link", true); err != nil {
		t.Fatal(err)
	}
	if exists, err := filesystem.Exists(ctx, "link"); err != nil || exists {
		t.Fatalf("link exists = %v, %v", exists, err)
	}
	data, err := os.ReadFile(filepath.Join(target, "kept.txt"))
	if err != nil || string(data) != "kept" {
		t.Fatalf("target content = %q, %v", data, err)
	}
}

func TestSFTPDeleteRecursively(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	ctx := context.Background()
	if err := filesystem.Mkdir(ctx, "tree"); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Mkdir(ctx, "tree/sub"); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Mkdir(ctx, "tree/sub/deep"); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFile(ctx, "tree/top.txt", []byte("top"), false); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFile(ctx, "tree/sub/deep/leaf.txt", []byte("leaf"), false); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "kept.txt"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "tree", "link")); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Delete(ctx, "tree", true); err != nil {
		t.Fatal(err)
	}
	if exists, err := filesystem.Exists(ctx, "tree"); err != nil || exists {
		t.Fatalf("tree exists = %v, %v", exists, err)
	}
	data, err := os.ReadFile(filepath.Join(target, "kept.txt"))
	if err != nil || string(data) != "kept" {
		t.Fatalf("symlink target content = %q, %v", data, err)
	}

	if err := filesystem.Mkdir(ctx, "empty"); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Delete(ctx, "empty", true); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFile(ctx, "plain.txt", []byte("plain"), false); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Delete(ctx, "plain.txt", false); err != nil {
		t.Fatal(err)
	}
	if exists, err := filesystem.Exists(ctx, "plain.txt"); err != nil || exists {
		t.Fatalf("plain.txt exists = %v, %v", exists, err)
	}
}

func TestSFTPHomeExpansion(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	ctx := context.Background()
	if err := filesystem.WriteFile(ctx, "~/home.txt", []byte("home"), false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "home.txt"))
	if err != nil || string(data) != "home" {
		t.Fatalf("home file = %q, %v", data, err)
	}
}

func TestSFTPChunkedUploadPreservesTargetMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not available on Windows")
	}
	for _, mode := range []os.FileMode{0o600, 0o755} {
		t.Run(mode.String(), func(t *testing.T) {
			filesystem, root := newTestFS(t, nil)
			remote := filepath.Join(root, "mode.bin")
			if err := os.WriteFile(remote, []byte("old"), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(remote, mode); err != nil {
				t.Fatal(err)
			}
			upload, err := chunked.New("00000000000000000000000001", remote, 4, 4)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte("new!"))
			if _, err := upload.WriteChunk(context.Background(), filesystem, chunked.Chunk{
				Index: 0, Size: 4, SHA256: hex.EncodeToString(digest[:]), Data: []byte("new!"),
			}); err != nil {
				t.Fatal(err)
			}
			part := chunked.StagingPath(remote, "00000000000000000000000001", "part-000000000000")
			if !strings.HasPrefix(filepath.Base(part), ".") {
				t.Fatalf("staging path is not hidden: %s", part)
			}
			info, err := os.Stat(part)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("staging mode = %v, %v", info.Mode(), err)
			}
			if _, err := upload.Commit(context.Background(), filesystem); err != nil {
				t.Fatal(err)
			}
			info, err = os.Stat(remote)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("committed mode = %v, %v; want %v", info.Mode(), err, mode)
			}
			assertRemoteContent(t, filesystem, remote, "new!")
		})
	}
}

func TestUploadDownloadResumeProgressAndEmptyFiles(t *testing.T) {
	filesystem, _ := newTestFS(t, nil)
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "source.bin")
	if err := os.WriteFile(source, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	var uploads []Progress
	transferred, err := filesystem.Upload(ctx, source, "upload.bin", TransferOptions{Resume: true, Progress: func(p Progress) { uploads = append(uploads, p) }})
	if err != nil || transferred != 10 {
		t.Fatalf("upload = %d, %v", transferred, err)
	}
	assertRemoteContent(t, filesystem, "upload.bin", "0123456789")
	assertFinalProgress(t, uploads, 10)

	if err := filesystem.WriteFile(ctx, "upload.bin", []byte("0123"), false); err != nil {
		t.Fatal(err)
	}
	transferred, err = filesystem.Upload(ctx, source, "upload.bin", TransferOptions{Resume: true})
	if err != nil || transferred != 10 {
		t.Fatalf("resumed upload = %d, %v", transferred, err)
	}
	assertRemoteContent(t, filesystem, "upload.bin", "0123456789")

	download := filepath.Join(dir, "download.bin")
	if err := os.WriteFile(download, []byte("012"), 0o600); err != nil {
		t.Fatal(err)
	}
	var downloads []Progress
	transferred, err = filesystem.Download(ctx, "upload.bin", download, TransferOptions{Resume: true, Progress: func(p Progress) { downloads = append(downloads, p) }})
	if err != nil || transferred != 10 {
		t.Fatalf("resumed download = %d, %v", transferred, err)
	}
	data, _ := os.ReadFile(download)
	if string(data) != "0123456789" {
		t.Fatalf("download = %q", data)
	}
	assertFinalProgress(t, downloads, 10)

	emptySource := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(emptySource, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := filesystem.Upload(ctx, emptySource, "empty-remote.bin", TransferOptions{}); err != nil {
		t.Fatal(err)
	}
	if size, err := filesystem.Size(ctx, "empty-remote.bin"); err != nil || size != 0 {
		t.Fatalf("empty upload size = %d, %v", size, err)
	}
	emptyDownload := filepath.Join(dir, "empty-download.bin")
	if _, err := filesystem.Download(ctx, "empty-remote.bin", emptyDownload, TransferOptions{}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(emptyDownload); err != nil || info.Size() != 0 {
		t.Fatalf("empty download = %+v, %v", info, err)
	}
}

func TestResumeRejectsOversizedTargets(t *testing.T) {
	filesystem, _ := newTestFS(t, nil)
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "small")
	if err := os.WriteFile(source, []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFile(ctx, "large", []byte("much larger"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := filesystem.Upload(ctx, source, "large", TransferOptions{Resume: true}); err == nil {
		t.Fatal("oversized remote resume target was truncated")
	}
	local := filepath.Join(dir, "large-local")
	if err := os.WriteFile(local, []byte("much larger locally"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := filesystem.Download(ctx, "large", local, TransferOptions{Resume: true}); err == nil {
		t.Fatal("oversized local resume target was truncated")
	}
}

func TestPackDownloadDelegatesPOSIXCommand(t *testing.T) {
	packErr := errors.New("stop after pack command")
	executor := &fakeExecutor{execErr: packErr, skipArchiveWrite: true}
	filesystem, _ := newTestFS(t, executor)
	executor.filesystem = filesystem
	if _, err := filesystem.PackDownload(context.Background(), "/srv/some dir", filepath.Join(t.TempDir(), "out.tar.gz"), TransferOptions{}); !errors.Is(err, packErr) {
		t.Fatalf("pack command error = %v, want delegated error", err)
	}
	if len(executor.commands) != 1 || !strings.Contains(executor.commands[0], "tar -czf '/tmp/nexterm-pack-") || !strings.Contains(executor.commands[0], "-C '/srv' -- 'some dir'") {
		t.Fatalf("POSIX pack command = %v", executor.commands)
	}
	if !strings.HasPrefix(executor.temporary, "/tmp/nexterm-pack-") || !strings.HasSuffix(executor.temporary, ".tar.gz") {
		t.Fatalf("POSIX temporary archive = %q", executor.temporary)
	}
}

func requirePOSIXArchiveNamespace(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("archive round-trip fixture requires a POSIX /tmp namespace; Windows runs command delegation coverage")
	}
}

func TestPackDownloadAndExtract(t *testing.T) {
	requirePOSIXArchiveNamespace(t)
	executor := &fakeExecutor{}
	filesystem, _ := newTestFS(t, executor)
	executor.filesystem = filesystem
	local := filepath.Join(t.TempDir(), "pack.tar.gz")
	transferred, err := filesystem.PackDownload(context.Background(), "some dir", local, TransferOptions{})
	if err != nil || transferred != int64(len("archive")) {
		t.Fatalf("pack download = %d, %v", transferred, err)
	}
	data, _ := os.ReadFile(local)
	if string(data) != "archive" {
		t.Fatalf("archive = %q", data)
	}
	if len(executor.commands) != 1 || !strings.Contains(executor.commands[0], "tar -czf") {
		t.Fatalf("pack commands = %v", executor.commands)
	}
	if executor.temporary == "" {
		t.Fatal("temporary archive was not recorded")
	}
	if exists, err := filesystem.Exists(context.Background(), executor.temporary); err != nil || exists {
		t.Fatalf("temporary archive remains = %v, %v", exists, err)
	}
	target, err := filesystem.Extract(context.Background(), "folder/a.tar.gz")
	if err != nil || target != "folder/a" {
		t.Fatalf("extract = %q, %v", target, err)
	}
	if !strings.Contains(executor.commands[1], "tar -xzf") {
		t.Fatalf("extract command = %q", executor.commands[1])
	}
}

func TestPackDownloadExpandsHomeBeforePacking(t *testing.T) {
	requirePOSIXArchiveNamespace(t)
	executor := &fakeExecutor{}
	filesystem, _ := newTestFS(t, executor)
	executor.filesystem = filesystem
	local := filepath.Join(t.TempDir(), "pack.tar.gz")
	if _, err := filesystem.PackDownload(context.Background(), "~/some dir", local, TransferOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(executor.commands) != 1 || strings.Contains(executor.commands[0], "'~/") {
		t.Fatalf("pack command kept an unexpanded home path = %v", executor.commands)
	}
	if exists, err := filesystem.Exists(context.Background(), executor.temporary); err != nil || exists {
		t.Fatalf("temporary archive remains = %v, %v", exists, err)
	}
}

func TestPackDownloadCleansPartialArchiveFailures(t *testing.T) {
	requirePOSIXArchiveNamespace(t)
	for _, test := range []struct {
		name     string
		execErr  error
		exitCode int
	}{
		{name: "exec error", execErr: fmt.Errorf("exec transport failed")},
		{name: "nonzero exit", exitCode: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := &fakeExecutor{execErr: test.execErr, exitCode: test.exitCode}
			filesystem, _ := newTestFS(t, executor)
			executor.filesystem = filesystem
			_, err := filesystem.PackDownload(context.Background(), "some dir", filepath.Join(t.TempDir(), "out.tar.gz"), TransferOptions{})
			if err == nil {
				t.Fatal("failed pack unexpectedly succeeded")
			}
			if executor.temporary == "" {
				t.Fatal("partial archive was not created by the test executor")
			}
			if exists, statErr := filesystem.Exists(context.Background(), executor.temporary); statErr != nil || exists {
				t.Fatalf("partial archive remains after failure = %v, %v", exists, statErr)
			}
		})
	}
}

func newTestFS(t *testing.T, executor Executor) (*FS, string) {
	t.Helper()
	return newTestFSWithOptions(t, executor)
}

func newTestFSWithOptions(t *testing.T, executor Executor, options ...sftp.ClientOption) (*FS, string) {
	t.Helper()
	root := t.TempDir()
	clientSide, serverSide := net.Pipe()
	server, err := sftp.NewServer(serverSide, sftp.WithServerWorkingDirectory(root))
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve() }()
	client, err := sftp.NewClientPipe(clientSide, clientSide, options...)
	if err != nil {
		clientSide.Close()
		serverSide.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		server.Close()
		clientSide.Close()
		serverSide.Close()
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
			t.Error("SFTP test server did not stop")
		}
	})
	return New(client, executor), root
}

func assertRemoteContent(t *testing.T, filesystem *FS, remotePath, expected string) {
	t.Helper()
	data, err := filesystem.ReadFile(context.Background(), remotePath, 1<<20)
	if err != nil || string(data) != expected {
		t.Fatalf("remote %s = %q, %v", remotePath, data, err)
	}
}

func assertFinalProgress(t *testing.T, progress []Progress, total int64) {
	t.Helper()
	if len(progress) == 0 {
		t.Fatal("no progress events")
	}
	last := progress[len(progress)-1]
	if !last.Done || last.Transferred != total || last.Total != total {
		t.Fatalf("final progress = %+v", last)
	}
}

type fakeExecutor struct {
	mu               sync.Mutex
	commands         []string
	filesystem       *FS
	temporary        string
	execErr          error
	exitCode         int
	skipArchiveWrite bool
}

func (e *fakeExecutor) Exec(ctx context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
	e.mu.Lock()
	e.commands = append(e.commands, command)
	e.mu.Unlock()
	const packPrefix = "umask 077 && tar -czf '"
	if strings.HasPrefix(command, packPrefix) {
		rest := strings.TrimPrefix(command, packPrefix)
		end := strings.Index(rest, "'")
		if end < 0 {
			return base.ExecResult{}, fmt.Errorf("unquoted pack output")
		}
		e.temporary = rest[:end]
		if !e.skipArchiveWrite {
			if err := e.filesystem.WriteFile(ctx, e.temporary, []byte("archive"), false); err != nil {
				return base.ExecResult{}, err
			}
		}
	}
	if e.execErr != nil {
		return base.ExecResult{}, e.execErr
	}
	code := e.exitCode
	return base.ExecResult{ExitCode: &code}, nil
}
