package local

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestDownloadRefusesSourceLargerThanMaxBytes(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "remote.bin")
	if err := os.WriteFile(source, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "local.bin")
	var progress []Progress
	transferred, err := Download(context.Background(), New(), source, target, TransferOptions{
		MaxBytes: 5,
		Progress: func(p Progress) { progress = append(progress, p) },
	})
	if !errors.Is(err, base.ErrTransferTooLarge) || transferred != 0 {
		t.Fatalf("Download = %d, %v; want ErrTransferTooLarge before any byte", transferred, err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused download left a target: %v", err)
	}
	if len(progress) == 0 || progress[len(progress)-1].Error == "" || !progress[len(progress)-1].Done {
		t.Fatalf("progress = %+v, want a terminal error event", progress)
	}
}

func TestDownloadWithinOrWithoutMaxBytesCompletes(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "remote.bin")
	if err := os.WriteFile(source, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int64{0, 6} {
		target := filepath.Join(dir, "local.bin")
		if transferred, err := Download(context.Background(), New(), source, target, TransferOptions{MaxBytes: limit}); err != nil || transferred != 6 {
			t.Fatalf("Download(limit %d) = %d, %v", limit, transferred, err)
		}
		assertFileContent(t, target, "abcdef")
	}
}

func TestPackDownloadAbandonsArchiveBeyondMaxBytes(t *testing.T) {
	tempDir := isolatePackTempDir(t)
	root := t.TempDir()
	// Random bytes do not compress, so the archive outgrows the cap while it is being written.
	payload := make([]byte, 256<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "noise.bin"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "out.tar.gz")
	_, err := New().PackDownload(context.Background(), root, destination, sshfs.TransferOptions{MaxBytes: 64 << 10})
	if !errors.Is(err, base.ErrTransferTooLarge) {
		t.Fatalf("PackDownload = %v, want ErrTransferTooLarge", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversize pack left a destination: %v", err)
	}
	assertNoPackTempArchives(t, tempDir)
}

func TestLimitWriterStopsAtLimit(t *testing.T) {
	var out bytes.Buffer
	writer := LimitWriter(&out, 5)
	if _, err := writer.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	n, err := writer.Write([]byte("defg"))
	if !errors.Is(err, base.ErrTransferTooLarge) || n != 2 || out.String() != "abcde" {
		t.Fatalf("limited write = %d, %v, %q; want 2 bytes then ErrTransferTooLarge", n, err, out.String())
	}
	if unlimited := LimitWriter(&out, 0); unlimited != io.Writer(&out) {
		t.Fatal("a zero limit must leave the writer unchanged")
	}
}
