package ssh

import (
	"context"
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

	"github.com/Hello-CTF/NexTerm/internal/fs/conditional"
	"github.com/Hello-CTF/NexTerm/internal/fs/remote"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// fileChannelServerArg makes the test binary serve a file channel on stdio,
// standing in for the helper's --files mode on the far side of a real sshd.
const fileChannelServerArg = "__nexterm_file_channel_test__"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == fileChannelServerArg {
		if err := remote.ServeStdio(context.Background(), os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type testHelperFiles struct {
	*remote.FileSystem
	mux *remote.Mux
}

func (f *testHelperFiles) Close() error { return f.mux.Close() }

// fileChannelProcesses counts the running file channel servers tagged token.
func fileChannelProcesses(t *testing.T, token string) int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skipf("process table unavailable: %v", err)
	}
	count := 0
	for _, entry := range entries {
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(string(cmdline), "\x00")
		if len(args) > 2 && args[1] == fileChannelServerArg && args[2] == token {
			count++
		}
	}
	return count
}

// A host whose sshd offers no SFTP subsystem still serves files through the
// file channel; the channel process on the host ends with the connection.
func TestFileChannelServesFilesWithoutSFTPOverOpenSSH(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the process check reads /proc")
	}
	fixture := startOpenSSHTestServer(t, 0, nil)
	store := NewMemoryHostKeyStore()
	if err := store.PutHostKey(context.Background(), newHostKey("127.0.0.1", fixture.port, fixture.hostKey), false); err != nil {
		t.Fatal(err)
	}
	client, err := Connect(context.Background(), Config{
		Host: "127.0.0.1", Port: fixture.port, User: fixture.user,
		Auth: AuthConfig{Method: AuthKey, KeyPEM: fixture.userKeyPEM}, HostKeys: store,
		ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect: %v: %s", err, fixture.sshdStderr())
	}
	defer client.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	token := fmt.Sprintf("token-%d", time.Now().UnixNano())
	command := shellQuote(executable) + " " + fileChannelServerArg + " " + token
	var changes sync.WaitGroup
	changes.Add(1)
	client.SetFileChannel(FileChannelConfig{
		Open: func(ctx context.Context) (FileChannelFS, error) {
			mux := remote.NewMux(func(context.Context) (net.Conn, error) {
				return client.OpenExecConn(context.Background(), command, base.ExecOptions{})
			})
			probe, err := mux.Open(ctx)
			if err != nil {
				_ = mux.Close()
				return nil, err
			}
			_ = probe.Close()
			return &testHelperFiles{FileSystem: remote.NewFileSystem(remote.Options{Dial: mux.Open, Exec: client.Exec, Channel: base.FileChannelHelper}), mux: mux}, nil
		},
		Changed: changes.Done,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	filesystem, err := client.FileSystem(ctx)
	if err != nil {
		t.Fatalf("file system without SFTP: %v", err)
	}
	if reporter, ok := filesystem.(base.FileChannelReporter); !ok || reporter.FileChannel() != base.FileChannelHelper {
		t.Fatalf("file system = %T, want the helper file channel", filesystem)
	}
	changes.Wait()
	if channel, reason := client.FileChannelStatus(ctx); channel != base.FileChannelHelper || reason != "" {
		t.Fatalf("channel status = %q, %q", channel, reason)
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "notes.txt")
	if err := filesystem.WriteFile(ctx, target, []byte("first"), false); err != nil {
		t.Fatal(err)
	}
	entries, err := filesystem.List(ctx, dir)
	if err != nil || len(entries) != 1 || entries[0].Name != "notes.txt" || entries[0].Size != 5 {
		t.Fatalf("listing = %+v, %v", entries, err)
	}
	writer := filesystem.(conditional.Writer)
	if err := writer.WriteFileVersion(ctx, target, []byte("second"), false, conditional.Absent()); !errors.Is(err, conditional.ErrVersionMismatch) {
		t.Fatalf("conditional create over an existing file = %v, want a version mismatch", err)
	}
	if err := writer.WriteFileVersion(ctx, filepath.Join(dir, "fresh.txt"), []byte("new"), false, conditional.Absent()); err != nil {
		t.Fatalf("conditional create = %v", err)
	}

	events := make(chan base.WatchEvent, 16)
	handle, err := filesystem.(base.FileWatcher).Watch(ctx, func(event base.WatchEvent) { events <- event }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Set([]string{dir}); err != nil {
		t.Fatal(err)
	}
	// The watch is in place once Set returned on the host; give the request
	// time to cross the channel before changing the directory.
	deadline := time.After(10 * time.Second)
	created := false
	for !created {
		if err := os.WriteFile(filepath.Join(dir, "made-outside.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		select {
		case event := <-events:
			if event.Dir == dir && (len(event.Names) == 0 || strings.Contains(strings.Join(event.Names, ","), "made-outside.txt")) {
				created = true
			}
		case <-time.After(500 * time.Millisecond):
		case <-deadline:
			t.Fatal("no change event for a file created on the host")
		}
	}
	_ = handle.Close()

	if count := fileChannelProcesses(t, token); count != 1 {
		t.Fatalf("file channel processes = %d, want one shared channel", count)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	for wait := time.Now().Add(10 * time.Second); fileChannelProcesses(t, token) != 0; {
		if time.Now().After(wait) {
			t.Fatal("the file channel process outlived the SSH connection")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
