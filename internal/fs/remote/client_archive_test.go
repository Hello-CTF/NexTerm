package remote

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestFileSystemPackDownloadCommandExpandsHome(t *testing.T) {
	var commands []string
	filesystem := NewFileSystem(Options{
		Dial: func(context.Context) (net.Conn, error) { return nil, errors.New("no bridge") },
		Exec: func(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
			commands = append(commands, command)
			code := 0
			return base.ExecResult{ExitCode: &code}, nil
		},
	})
	for _, testCase := range []struct {
		path   string
		suffix string
	}{
		{"~/packs", ` -C "$HOME" -- 'packs'`},
		{"~", ` -C "$HOME" -- '.'`},
		{"/data/dir", ` -C '/data' -- 'dir'`},
		{"/~", ` -C '/' -- '~'`},
		{"-weird", ` -C '.' -- './-weird'`},
	} {
		if _, err := filesystem.PackDownload(context.Background(), testCase.path, filepath.Join(t.TempDir(), "x.tar.gz"), sshfs.TransferOptions{}); err == nil {
			t.Fatalf("PackDownload(%q) unexpectedly succeeded without a file bridge", testCase.path)
		}
		command := commands[len(commands)-1]
		cut := strings.Index(command, " -C ")
		if !strings.HasPrefix(command, "umask 077 && tar -czf '/tmp/nexterm-pack-") || cut < 0 {
			t.Fatalf("PackDownload(%q) command = %q, want a random /tmp tar target", testCase.path, command)
		}
		if command[cut:] != testCase.suffix {
			t.Errorf("PackDownload(%q) command tail = %q, want %q", testCase.path, command[cut:], testCase.suffix)
		}
	}
}

func TestFileSystemExtractExpandsHome(t *testing.T) {
	var commands []string
	filesystem := NewFileSystem(Options{Exec: func(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
		commands = append(commands, command)
		code := 0
		return base.ExecResult{ExitCode: &code}, nil
	}})
	for _, testCase := range []struct {
		path    string
		target  string
		command string
	}{
		{"~/x.tar", "~", `tar -xf "$HOME"'/x.tar' -C "$HOME"`},
		{"~/packs/x.tar", "~/packs", `tar -xf "$HOME"'/packs/x.tar' -C "$HOME"'/packs'`},
		{"~", ".", `tar -xf "$HOME" -C '.'`},
		{"/data/x.tar", "/data", `tar -xf '/data/x.tar' -C '/data'`},
	} {
		target, err := filesystem.Extract(context.Background(), testCase.path)
		if err != nil {
			t.Fatalf("Extract(%q) = %v", testCase.path, err)
		}
		if target != testCase.target {
			t.Errorf("Extract(%q) target = %q, want %q", testCase.path, target, testCase.target)
		}
		if commands[len(commands)-1] != testCase.command {
			t.Errorf("Extract(%q) command = %q, want %q", testCase.path, commands[len(commands)-1], testCase.command)
		}
	}
}
