package local

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func TestDownloadRejectsHomeSameFileAndHardlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	filesystem := New()
	source := filepath.Join(home, "same")
	hardlink := filepath.Join(home, "hardlink")
	if err := os.WriteFile(source, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, hardlink); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"~/same", "~/hardlink"} {
		if _, err := Download(context.Background(), filesystem, "~/same", target, TransferOptions{}); err == nil {
			t.Fatalf("Download(~same, %s) unexpectedly succeeded", target)
		}
	}
	assertBytes(t, source, []byte("keep"))
	assertBytes(t, hardlink, []byte("keep"))
}

func TestUploadSourceChangesAfterStat(t *testing.T) {
	t.Run("shrink", func(t *testing.T) {
		dir := t.TempDir()
		source := filepath.Join(dir, "source")
		remote := filepath.Join(dir, "remote")
		if err := os.WriteFile(source, []byte("abcdef"), 0o600); err != nil {
			t.Fatal(err)
		}
		destination := &mutateOpenWriteFS{FileSystem: New(), mutate: func() {
			if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}}
		var progress []Progress
		transferred, err := Upload(context.Background(), source, destination, remote, TransferOptions{Progress: func(p Progress) {
			progress = append(progress, p)
		}})
		if !errors.Is(err, io.ErrUnexpectedEOF) || transferred != 1 {
			t.Fatalf("shrunk upload = %d, %v", transferred, err)
		}
		assertBytes(t, remote, []byte("x"))
		assertFailedProgress(t, progress, 1, 6)
	})
	t.Run("growth", func(t *testing.T) {
		dir := t.TempDir()
		source := filepath.Join(dir, "source")
		remote := filepath.Join(dir, "remote")
		if err := os.WriteFile(source, []byte("abcdef"), 0o600); err != nil {
			t.Fatal(err)
		}
		destination := &mutateOpenWriteFS{FileSystem: New(), mutate: func() {
			if err := os.WriteFile(source, []byte("abcdefgh"), 0o600); err != nil {
				t.Fatal(err)
			}
		}}
		transferred, err := Upload(context.Background(), source, destination, remote, TransferOptions{})
		if err != nil || transferred != 6 {
			t.Fatalf("grown upload = %d, %v", transferred, err)
		}
		assertBytes(t, remote, []byte("abcdef"))
	})
}

func TestDownloadShortAndGrowingSources(t *testing.T) {
	t.Run("short", func(t *testing.T) {
		source := &staticReaderFS{reader: &sizedStringReader{Reader: strings.NewReader("abc"), size: 6}}
		local := filepath.Join(t.TempDir(), "local")
		var progress []Progress
		transferred, err := Download(context.Background(), source, "remote", local, TransferOptions{Progress: func(p Progress) {
			progress = append(progress, p)
		}})
		if !errors.Is(err, io.ErrUnexpectedEOF) || transferred != 3 {
			t.Fatalf("short download = %d, %v", transferred, err)
		}
		assertBytes(t, local, []byte("abc"))
		assertFailedProgress(t, progress, 3, 6)
	})
	t.Run("growth", func(t *testing.T) {
		source := &staticReaderFS{reader: &sizedStringReader{Reader: strings.NewReader("abcdefgh"), size: 6}}
		local := filepath.Join(t.TempDir(), "local")
		transferred, err := Download(context.Background(), source, "remote", local, TransferOptions{})
		if err != nil || transferred != 6 {
			t.Fatalf("growing download = %d, %v", transferred, err)
		}
		assertBytes(t, local, []byte("abcdef"))
	})
}

func TestResumeExactAndOversizedTargets(t *testing.T) {
	t.Run("upload exact", func(t *testing.T) {
		dir := t.TempDir()
		source := filepath.Join(dir, "source")
		remote := filepath.Join(dir, "remote")
		if err := os.WriteFile(source, []byte("abc"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(remote, []byte("zzz"), 0o600); err != nil {
			t.Fatal(err)
		}
		var progress []Progress
		transferred, err := Upload(context.Background(), source, New(), remote, TransferOptions{Resume: true, Progress: func(p Progress) {
			progress = append(progress, p)
		}})
		if err != nil || transferred != 3 {
			t.Fatalf("exact upload = %d, %v", transferred, err)
		}
		assertBytes(t, remote, []byte("zzz"))
		assertDoneProgress(t, progress, 3)
	})
	t.Run("download exact", func(t *testing.T) {
		dir := t.TempDir()
		remote := filepath.Join(dir, "remote")
		local := filepath.Join(dir, "local")
		if err := os.WriteFile(remote, []byte("abc"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(local, []byte("zzz"), 0o600); err != nil {
			t.Fatal(err)
		}
		var progress []Progress
		transferred, err := Download(context.Background(), New(), remote, local, TransferOptions{Resume: true, Progress: func(p Progress) {
			progress = append(progress, p)
		}})
		if err != nil || transferred != 3 {
			t.Fatalf("exact download = %d, %v", transferred, err)
		}
		assertBytes(t, local, []byte("zzz"))
		assertDoneProgress(t, progress, 3)
	})
	t.Run("upload oversized", func(t *testing.T) {
		dir := t.TempDir()
		source := filepath.Join(dir, "source")
		remote := filepath.Join(dir, "remote")
		if err := os.WriteFile(source, []byte("abc"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(remote, []byte("keep-oversized"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Upload(context.Background(), source, New(), remote, TransferOptions{Resume: true}); err == nil || !strings.Contains(err.Error(), "larger") {
			t.Fatalf("oversized upload error = %v", err)
		}
		assertBytes(t, remote, []byte("keep-oversized"))
	})
	t.Run("download oversized", func(t *testing.T) {
		dir := t.TempDir()
		remote := filepath.Join(dir, "remote")
		local := filepath.Join(dir, "local")
		if err := os.WriteFile(remote, []byte("abc"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(local, []byte("keep-oversized"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Download(context.Background(), New(), remote, local, TransferOptions{Resume: true}); err == nil || !strings.Contains(err.Error(), "larger") {
			t.Fatalf("oversized download error = %v", err)
		}
		assertBytes(t, local, []byte("keep-oversized"))
	})
	t.Run("empty exact", func(t *testing.T) {
		dir := t.TempDir()
		source := filepath.Join(dir, "source")
		remote := filepath.Join(dir, "remote")
		if err := os.WriteFile(source, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(remote, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		var progress []Progress
		transferred, err := Upload(context.Background(), source, New(), remote, TransferOptions{Resume: true, Progress: func(p Progress) {
			progress = append(progress, p)
		}})
		if err != nil || transferred != 0 {
			t.Fatalf("empty exact upload = %d, %v", transferred, err)
		}
		assertDoneProgress(t, progress, 0)
	})
}

func TestFinishTransferDoneRequiresSyncAndClose(t *testing.T) {
	for _, test := range []struct {
		name     string
		syncErr  error
		closeErr error
	}{
		{"sync", errors.New("sync failed"), nil},
		{"close", nil, errors.New("close failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := &boundaryWriter{syncErr: test.syncErr, closeErr: test.closeErr}
			var progress []Progress
			_, err := finishTransfer(context.Background(), strings.NewReader("abc"), target, 0, 3, TransferOptions{Progress: func(p Progress) {
				progress = append(progress, p)
			}})
			if err == nil {
				t.Fatal("failure unexpectedly succeeded")
			}
			assertNoDoneProgress(t, progress)
		})
	}
}

type mutateOpenWriteFS struct {
	*FileSystem
	mutate func()
}

func (f *mutateOpenWriteFS) OpenWrite(ctx context.Context, path string, appendMode bool) (base.RemoteWriter, error) {
	f.mutate()
	return f.FileSystem.OpenWrite(ctx, path, appendMode)
}

type staticReaderFS struct {
	base.FileSystem
	reader base.RemoteReader
}

func (f *staticReaderFS) OpenRead(context.Context, string) (base.RemoteReader, error) {
	return f.reader, nil
}

type sizedStringReader struct {
	*strings.Reader
	size int64
}

func (r *sizedStringReader) Size() int64 {
	return r.size
}

func (r *sizedStringReader) Close() error {
	return nil
}

type boundaryWriter struct {
	bytes.Buffer
	syncErr  error
	closeErr error
	synced   bool
	closed   bool
}

func (w *boundaryWriter) Sync() error {
	w.synced = true
	return w.syncErr
}

func (w *boundaryWriter) Close() error {
	w.closed = true
	return w.closeErr
}

func assertNoDoneProgress(t *testing.T, progress []Progress) {
	t.Helper()
	for _, event := range progress {
		if event.Done {
			t.Fatalf("unexpected done progress: %#v", event)
		}
	}
}

func assertDoneProgress(t *testing.T, progress []Progress, transferred int64) {
	t.Helper()
	if len(progress) == 0 {
		t.Fatal("missing final progress")
	}
	last := progress[len(progress)-1]
	if !last.Done || last.Error != "" || last.Transferred != transferred || last.Total != transferred {
		t.Fatalf("final progress = %#v", last)
	}
}

func assertFailedProgress(t *testing.T, progress []Progress, transferred, total int64) {
	t.Helper()
	if len(progress) == 0 {
		t.Fatal("missing failure progress")
	}
	last := progress[len(progress)-1]
	if !last.Done || last.Error == "" || last.Transferred != transferred || last.Total != total {
		t.Fatalf("failure progress = %#v", last)
	}
}
