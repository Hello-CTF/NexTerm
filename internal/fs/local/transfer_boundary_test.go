package local

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
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
		if _, statErr := os.Stat(remote); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed non-resume upload exposed a partial target: %v", statErr)
		}
		artifacts, globErr := filepath.Glob(filepath.Join(filepath.Dir(remote), "."+filepath.Base(remote)+".nexterm-*.commit"))
		if globErr != nil || len(artifacts) != 0 {
			t.Fatalf("failed upload artifacts = %v, %v", artifacts, globErr)
		}
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
	t.Run("mtime", func(t *testing.T) {
		dir := t.TempDir()
		source := filepath.Join(dir, "source")
		remote := filepath.Join(dir, "remote")
		if err := os.WriteFile(source, []byte("abcdef"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(source)
		if err != nil {
			t.Fatal(err)
		}
		destination := &mutateOpenWriteFS{FileSystem: New(), mutate: func() {
			changed := info.ModTime().Add(1e9)
			if err := os.Chtimes(source, changed, changed); err != nil {
				t.Fatal(err)
			}
		}}
		transferred, err := Upload(context.Background(), source, destination, remote, TransferOptions{})
		if err != nil || transferred != 6 {
			t.Fatalf("mtime-only changed upload = %d, %v", transferred, err)
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
		source := &staticReaderFS{reader: &sizedStringReader{Reader: strings.NewReader("abcdefgh"), size: 6}, finalSize: 8}
		local := filepath.Join(t.TempDir(), "local")
		transferred, err := Download(context.Background(), source, "remote", local, TransferOptions{})
		if err != nil || transferred != 6 {
			t.Fatalf("growing download = %d, %v", transferred, err)
		}
		assertBytes(t, local, []byte("abcdef"))
	})
	t.Run("mtime", func(t *testing.T) {
		dir := t.TempDir()
		remote := filepath.Join(dir, "remote")
		local := filepath.Join(dir, "local")
		if err := os.WriteFile(remote, []byte("abcdef"), 0o600); err != nil {
			t.Fatal(err)
		}
		source := &changedSourceMtimeFS{FileSystem: New()}
		transferred, err := Download(context.Background(), source, remote, local, TransferOptions{})
		if err != nil || transferred != 6 {
			t.Fatalf("mtime-only changed download = %d, %v", transferred, err)
		}
		assertBytes(t, local, []byte("abcdef"))
	})
}

func TestResumeExactAndOversizedTargets(t *testing.T) {
	t.Run("upload exact mismatch", func(t *testing.T) {
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
		_, err := Upload(context.Background(), source, New(), remote, TransferOptions{Resume: true, Progress: func(p Progress) {
			progress = append(progress, p)
		}})
		if err == nil || !strings.Contains(err.Error(), "match") {
			t.Fatalf("exact mismatched upload error = %v", err)
		}
		assertBytes(t, remote, []byte("zzz"))
		assertFailedProgress(t, progress, 3, 3)
		if countDone(progress) != 1 {
			t.Fatalf("terminal events = %d, want exactly one", countDone(progress))
		}
	})
	t.Run("download exact mismatch", func(t *testing.T) {
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
		_, err := Download(context.Background(), New(), remote, local, TransferOptions{Resume: true, Progress: func(p Progress) {
			progress = append(progress, p)
		}})
		if err == nil || !strings.Contains(err.Error(), "match") {
			t.Fatalf("exact mismatched download error = %v", err)
		}
		assertBytes(t, local, []byte("zzz"))
		assertFailedProgress(t, progress, 3, 3)
		if countDone(progress) != 1 {
			t.Fatalf("terminal events = %d, want exactly one", countDone(progress))
		}
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
		if _, err := Upload(context.Background(), source, New(), remote, TransferOptions{Resume: true}); err == nil || !strings.Contains(err.Error(), "larger") || !errors.Is(err, ErrResumeMismatch) {
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

func TestAtomicUploadFailureDoesNotCreateResumePartial(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	remote := filepath.Join(dir, "remote")
	if err := os.WriteFile(source, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remote, []byte("keep-oversized"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeErr := errors.New("write failed")
	destination := &failingWriteFS{FileSystem: New(), err: writeErr}
	if _, err := Upload(context.Background(), source, destination, remote, TransferOptions{}); !errors.Is(err, writeErr) {
		t.Fatalf("initial atomic upload = %v", err)
	}
	assertBytes(t, remote, []byte("keep-oversized"))
	artifacts, err := filepath.Glob(filepath.Join(dir, ".remote.nexterm-*.commit"))
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("failed upload artifacts = %v, %v", artifacts, err)
	}
	if _, err := Upload(context.Background(), source, New(), remote, TransferOptions{Resume: true}); !errors.Is(err, ErrResumeMismatch) {
		t.Fatalf("oversized resume = %v", err)
	}
	assertBytes(t, remote, []byte("keep-oversized"))
	if _, err := Upload(context.Background(), source, New(), remote, TransferOptions{}); err != nil {
		t.Fatalf("full restart = %v", err)
	}
	assertBytes(t, remote, []byte("abc"))
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

func TestResumeRejectsMismatchedPrefixWithoutWriting(t *testing.T) {
	data := []byte("abcdefgh")
	t.Run("upload", func(t *testing.T) {
		dir := t.TempDir()
		source := filepath.Join(dir, "source")
		remote := filepath.Join(dir, "remote")
		if err := os.WriteFile(source, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(remote, []byte("abcX"), 0o600); err != nil {
			t.Fatal(err)
		}
		destination := &openWriteSpyFS{FileSystem: New()}
		var progress []Progress
		_, err := Upload(context.Background(), source, destination, remote, TransferOptions{Resume: true, Progress: func(p Progress) {
			progress = append(progress, p)
		}})
		if err == nil || !strings.Contains(err.Error(), "match") {
			t.Fatalf("mismatched upload error = %v", err)
		}
		if destination.opened {
			t.Fatal("mismatched upload opened the target for writing")
		}
		assertBytes(t, remote, []byte("abcX"))
		assertFailedProgress(t, progress, 4, int64(len(data)))
		if countDone(progress) != 1 {
			t.Fatalf("terminal events = %d, want exactly one", countDone(progress))
		}
	})
	t.Run("download nonseekable", func(t *testing.T) {
		dir := t.TempDir()
		remote := filepath.Join(dir, "remote")
		local := filepath.Join(dir, "local")
		if err := os.WriteFile(remote, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(local, []byte("abcX"), 0o600); err != nil {
			t.Fatal(err)
		}
		var progress []Progress
		_, err := Download(context.Background(), &nonSeekableFS{FileSystem: New()}, remote, local, TransferOptions{Resume: true, Progress: func(p Progress) {
			progress = append(progress, p)
		}})
		if err == nil || !strings.Contains(err.Error(), "match") {
			t.Fatalf("mismatched download error = %v", err)
		}
		assertBytes(t, local, []byte("abcX"))
		assertFailedProgress(t, progress, 4, int64(len(data)))
		if countDone(progress) != 1 {
			t.Fatalf("terminal events = %d, want exactly one", countDone(progress))
		}
	})
}

func TestDownloadResumeNonSeekable(t *testing.T) {
	dir := t.TempDir()
	data := transferData(transferChunkSize + 137)
	remote := filepath.Join(dir, "remote")
	local := filepath.Join(dir, "local")
	if err := os.WriteFile(remote, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, data[:transferChunkSize-31], 0o600); err != nil {
		t.Fatal(err)
	}
	transferred, err := Download(context.Background(), &nonSeekableFS{FileSystem: New()}, remote, local, TransferOptions{Resume: true})
	if err != nil || transferred != int64(len(data)) {
		t.Fatalf("nonseekable download = %d, %v", transferred, err)
	}
	assertBytes(t, local, data)
}

func TestUploadResumeRejectsSourceChanges(t *testing.T) {
	for _, test := range []struct {
		name        string
		replacement string
		changeMTime bool
	}{
		{name: "growth", replacement: "abcdefgh"},
		{name: "same size", replacement: "ABCDEF", changeMTime: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source")
			remote := filepath.Join(dir, "remote")
			if err := os.WriteFile(source, []byte("abcdef"), 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			destination := &mutateOpenWriteFS{FileSystem: New(), mutate: func() {
				if err := os.WriteFile(source, []byte(test.replacement), 0o600); err != nil {
					t.Fatal(err)
				}
				if test.changeMTime {
					changed := info.ModTime().Add(1e9)
					if err := os.Chtimes(source, changed, changed); err != nil {
						t.Fatal(err)
					}
				}
			}}
			var progress []Progress
			_, err = Upload(context.Background(), source, destination, remote, TransferOptions{Resume: true, Progress: func(p Progress) {
				progress = append(progress, p)
			}})
			if err == nil || !strings.Contains(err.Error(), "source changed") {
				t.Fatalf("changed source upload error = %v", err)
			}
			assertFailedProgress(t, progress, 6, 6)
			if countDone(progress) != 1 {
				t.Fatalf("terminal events = %d, want exactly one", countDone(progress))
			}
		})
	}
}

func TestDownloadResumeRejectsSourceSizeChange(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote")
	local := filepath.Join(dir, "local")
	if err := os.WriteFile(remote, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := &changedSourceSizeFS{FileSystem: New(), size: 7}
	var progress []Progress
	_, err := Download(context.Background(), source, remote, local, TransferOptions{Resume: true, Progress: func(p Progress) {
		progress = append(progress, p)
	}})
	if err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatalf("changed source download error = %v", err)
	}
	assertFailedProgress(t, progress, 6, 6)
	if countDone(progress) != 1 {
		t.Fatalf("terminal events = %d, want exactly one", countDone(progress))
	}
}

func TestDownloadResumeRejectsSourceModificationTimeChange(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote")
	local := filepath.Join(dir, "local")
	if err := os.WriteFile(remote, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := &changedSourceMtimeFS{FileSystem: New()}
	var progress []Progress
	_, err := Download(context.Background(), source, remote, local, TransferOptions{Resume: true, Progress: func(p Progress) {
		progress = append(progress, p)
	}})
	if err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatalf("changed source download error = %v", err)
	}
	assertFailedProgress(t, progress, 6, 6)
	if countDone(progress) != 1 {
		t.Fatalf("terminal events = %d, want exactly one", countDone(progress))
	}
}

func TestUploadResumeRequiresFinalSizeAndChecksum(t *testing.T) {
	sizeErr := errors.New("final size failed")
	checksumErr := errors.New("final checksum failed")
	for _, test := range []struct {
		name        string
		sizeErr     error
		checksumErr error
		openReadErr error
		want        error
	}{
		{name: "size", sizeErr: sizeErr, want: sizeErr},
		{name: "checksum", checksumErr: checksumErr, want: checksumErr},
		{name: "no checksum capability", checksumErr: base.ErrUnsupported, openReadErr: base.ErrUnsupported, want: base.ErrUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source")
			remote := filepath.Join(dir, "remote")
			data := []byte("abcdef")
			if err := os.WriteFile(source, data, 0o600); err != nil {
				t.Fatal(err)
			}
			destination := &verificationFS{FileSystem: New(), sizeErr: test.sizeErr, checksumErr: test.checksumErr, openReadErr: test.openReadErr}
			var progress []Progress
			_, err := Upload(context.Background(), source, destination, remote, TransferOptions{Resume: true, Progress: func(p Progress) {
				progress = append(progress, p)
			}})
			if !errors.Is(err, test.want) {
				t.Fatalf("final verification error = %v, want %v", err, test.want)
			}
			assertBytes(t, remote, data)
			assertFailedProgress(t, progress, 6, 6)
			if countDone(progress) != 1 {
				t.Fatalf("terminal events = %d, want exactly one", countDone(progress))
			}
		})
	}
}

func TestUploadResumeChecksumFallbackAndOrdinaryCompatibility(t *testing.T) {
	data := []byte("abcdef")
	t.Run("sequential fallback", func(t *testing.T) {
		dir := t.TempDir()
		source := filepath.Join(dir, "source")
		remote := filepath.Join(dir, "remote")
		if err := os.WriteFile(source, data, 0o600); err != nil {
			t.Fatal(err)
		}
		destination := &verificationFS{FileSystem: New(), checksumErr: base.ErrUnsupported}
		if _, err := Upload(context.Background(), source, destination, remote, TransferOptions{Resume: true}); err != nil {
			t.Fatalf("sequential checksum fallback = %v", err)
		}
		assertBytes(t, remote, data)
		if destination.checksumCalls != 1 {
			t.Fatalf("checksum calls = %d, want one", destination.checksumCalls)
		}
	})
	t.Run("ordinary transfer", func(t *testing.T) {
		dir := t.TempDir()
		source := filepath.Join(dir, "source")
		remote := filepath.Join(dir, "remote")
		if err := os.WriteFile(source, data, 0o600); err != nil {
			t.Fatal(err)
		}
		destination := &verificationFS{FileSystem: New(), checksumErr: errors.New("checksum unavailable"), openReadErr: base.ErrUnsupported}
		transferred, err := Upload(context.Background(), source, destination, remote, TransferOptions{})
		if err != nil || transferred != int64(len(data)) {
			t.Fatalf("ordinary upload = %d, %v", transferred, err)
		}
		assertBytes(t, remote, data)
		if destination.checksumCalls != 0 {
			t.Fatalf("ordinary upload made %d checksum calls", destination.checksumCalls)
		}
	})
}

func TestOrdinaryTransfersDoNotRequireSizeOrParentListing(t *testing.T) {
	data := []byte("abcdef")
	t.Run("upload size unsupported", func(t *testing.T) {
		dir := t.TempDir()
		source := filepath.Join(dir, "source")
		remote := filepath.Join(dir, "remote")
		if err := os.WriteFile(source, data, 0o600); err != nil {
			t.Fatal(err)
		}
		destination := &verificationFS{FileSystem: New(), sizeErr: base.ErrUnsupported}
		transferred, err := Upload(context.Background(), source, destination, remote, TransferOptions{})
		if err != nil || transferred != int64(len(data)) {
			t.Fatalf("ordinary upload without size = %d, %v", transferred, err)
		}
		assertBytes(t, remote, data)
		if destination.sizeCalls != 0 {
			t.Fatalf("ordinary upload made %d size calls", destination.sizeCalls)
		}
	})
	t.Run("download size unsupported", func(t *testing.T) {
		dir := t.TempDir()
		remote := filepath.Join(dir, "remote")
		local := filepath.Join(dir, "local")
		if err := os.WriteFile(remote, data, 0o600); err != nil {
			t.Fatal(err)
		}
		source := &verificationFS{FileSystem: New(), sizeErr: base.ErrUnsupported}
		transferred, err := Download(context.Background(), source, remote, local, TransferOptions{})
		if err != nil || transferred != int64(len(data)) {
			t.Fatalf("ordinary download without size = %d, %v", transferred, err)
		}
		assertBytes(t, local, data)
		if source.sizeCalls != 0 || source.listCalls != 0 {
			t.Fatalf("ordinary download made %d size calls and %d list calls", source.sizeCalls, source.listCalls)
		}
	})
	t.Run("download parent cannot list", func(t *testing.T) {
		dir := t.TempDir()
		remote := filepath.Join(dir, "remote")
		local := filepath.Join(dir, "local")
		if err := os.WriteFile(remote, data, 0o600); err != nil {
			t.Fatal(err)
		}
		source := &verificationFS{FileSystem: New(), listErr: errors.New("parent cannot be listed")}
		transferred, err := Download(context.Background(), source, remote, local, TransferOptions{})
		if err != nil || transferred != int64(len(data)) {
			t.Fatalf("ordinary download without parent listing = %d, %v", transferred, err)
		}
		assertBytes(t, local, data)
		if source.sizeCalls != 0 || source.listCalls != 0 {
			t.Fatalf("ordinary download made %d size calls and %d list calls", source.sizeCalls, source.listCalls)
		}
	})
}

func TestDownloadResumeWithoutOptionalMetadata(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote")
	local := filepath.Join(dir, "local")
	if err := os.WriteFile(remote, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := &verificationFS{FileSystem: New(), listErr: errors.New("metadata unavailable")}
	var progress []Progress
	transferred, err := Download(context.Background(), source, remote, local, TransferOptions{Resume: true, Progress: func(p Progress) {
		progress = append(progress, p)
	}})
	if err != nil || transferred != 6 {
		t.Fatalf("resume without optional metadata = %d, %v", transferred, err)
	}
	assertBytes(t, local, []byte("abcdef"))
	assertDoneProgress(t, progress, 6)
	if source.listCalls != 1 || source.sizeCalls != 1 {
		t.Fatalf("metadata calls = %d list and %d size, want one each", source.listCalls, source.sizeCalls)
	}
}

func TestUploadResumeChecksFinalContent(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	remote := filepath.Join(dir, "remote")
	if err := os.WriteFile(source, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := &mutateAfterCloseFS{FileSystem: New(), mutate: func() {
		if err := os.WriteFile(remote, []byte("ABCdef"), 0o600); err != nil {
			t.Fatal(err)
		}
	}}
	var progress []Progress
	_, err := Upload(context.Background(), source, destination, remote, TransferOptions{Resume: true, Progress: func(p Progress) {
		progress = append(progress, p)
	}})
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("mutated target upload error = %v", err)
	}
	assertBytes(t, remote, []byte("ABCdef"))
	assertFailedProgress(t, progress, 6, 6)
	if countDone(progress) != 1 {
		t.Fatalf("terminal events = %d, want exactly one", countDone(progress))
	}
}

func TestUploadResumeCancellationDuringVerification(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	remote := filepath.Join(dir, "remote")
	if err := os.WriteFile(source, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remote, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	destination := &cancelOnReadFS{FileSystem: New(), cancel: cancel}
	called := false
	_, err := Upload(ctx, source, destination, remote, TransferOptions{Resume: true, Progress: func(Progress) { called = true }})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled resume error = %v", err)
	}
	if destination.opened {
		t.Fatal("canceled verification opened the target for writing")
	}
	if called {
		t.Fatal("canceled resume emitted terminal progress")
	}
	assertBytes(t, remote, []byte("abc"))
}

func TestDownloadSymlinkSource(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(remote, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(remote, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, resume := range []bool{false, true} {
		t.Run(strconv.FormatBool(resume), func(t *testing.T) {
			local := filepath.Join(dir, "local-"+strconv.FormatBool(resume))
			transferred, err := Download(context.Background(), New(), link, local, TransferOptions{Resume: resume})
			if err != nil || transferred != 6 {
				t.Fatalf("symlink download = %d, %v", transferred, err)
			}
			assertBytes(t, local, []byte("abcdef"))
		})
	}
}

func TestDownloadResumeEmpty(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote")
	local := filepath.Join(dir, "local")
	if err := os.WriteFile(remote, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var progress []Progress
	transferred, err := Download(context.Background(), New(), remote, local, TransferOptions{Resume: true, Progress: func(p Progress) {
		progress = append(progress, p)
	}})
	if err != nil || transferred != 0 {
		t.Fatalf("empty download = %d, %v", transferred, err)
	}
	assertDoneProgress(t, progress, 0)
	if countDone(progress) != 1 {
		t.Fatalf("terminal events = %d, want exactly one", countDone(progress))
	}
}

func TestUploadResumeCancellationAfterChecksum(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	remote := filepath.Join(dir, "remote")
	if err := os.WriteFile(source, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	destination := &cancelChecksumFS{FileSystem: New(), cancel: cancel}
	called := false
	_, err := Upload(ctx, source, destination, remote, TransferOptions{Resume: true, Progress: func(Progress) { called = true }})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled final verification error = %v", err)
	}
	if called {
		t.Fatal("canceled final verification emitted terminal progress")
	}
	assertBytes(t, remote, []byte("abcdef"))
}

type cancelChecksumFS struct {
	*FileSystem
	cancel context.CancelFunc
}

func (f *cancelChecksumFS) Checksum(ctx context.Context, path, algorithm string) (string, error) {
	sum, err := f.FileSystem.Checksum(ctx, path, algorithm)
	f.cancel()
	return sum, err
}

type openWriteSpyFS struct {
	*FileSystem
	opened bool
}

func (f *openWriteSpyFS) OpenWrite(ctx context.Context, path string, appendMode bool) (base.RemoteWriter, error) {
	f.opened = true
	return f.FileSystem.OpenWrite(ctx, path, appendMode)
}

type nonSeekableFS struct {
	*FileSystem
}

func (f *nonSeekableFS) OpenRead(ctx context.Context, path string) (base.RemoteReader, error) {
	reader, err := f.FileSystem.OpenRead(ctx, path)
	if err != nil {
		return nil, err
	}
	return &nonSeekableReader{RemoteReader: reader}, nil
}

type nonSeekableReader struct {
	base.RemoteReader
}

type changedSourceSizeFS struct {
	*FileSystem
	size int64
}

func (f *changedSourceSizeFS) Size(context.Context, string) (int64, error) {
	return f.size, nil
}

type changedSourceMtimeFS struct {
	*FileSystem
	listCalls int
}

func (f *changedSourceMtimeFS) List(ctx context.Context, path string) ([]base.FileEntry, error) {
	entries, err := f.FileSystem.List(ctx, path)
	f.listCalls++
	if f.listCalls > 1 {
		for index := range entries {
			entries[index].ModTime = entries[index].ModTime.Add(1e9)
		}
	}
	return entries, err
}

type verificationFS struct {
	*FileSystem
	sizeErr       error
	listErr       error
	checksumErr   error
	openReadErr   error
	sizeCalls     int
	listCalls     int
	checksumCalls int
}

func (f *verificationFS) Size(ctx context.Context, path string) (int64, error) {
	f.sizeCalls++
	size, err := f.FileSystem.Size(ctx, path)
	if err != nil {
		return 0, err
	}
	if f.sizeErr != nil {
		return 0, f.sizeErr
	}
	return size, nil
}

func (f *verificationFS) List(ctx context.Context, path string) ([]base.FileEntry, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.FileSystem.List(ctx, path)
}

func (f *verificationFS) Checksum(ctx context.Context, path, algorithm string) (string, error) {
	f.checksumCalls++
	if f.checksumErr != nil {
		return "", f.checksumErr
	}
	return f.FileSystem.Checksum(ctx, path, algorithm)
}

func (f *verificationFS) OpenRead(ctx context.Context, path string) (base.RemoteReader, error) {
	if f.openReadErr != nil {
		return nil, f.openReadErr
	}
	return f.FileSystem.OpenRead(ctx, path)
}

type mutateAfterCloseFS struct {
	*FileSystem
	mutate func()
}

func (f *mutateAfterCloseFS) OpenWrite(ctx context.Context, path string, appendMode bool) (base.RemoteWriter, error) {
	writer, err := f.FileSystem.OpenWrite(ctx, path, appendMode)
	if err != nil {
		return nil, err
	}
	return &mutateAfterCloseWriter{RemoteWriter: writer, mutate: f.mutate}, nil
}

type mutateAfterCloseWriter struct {
	base.RemoteWriter
	mutate func()
}

func (w *mutateAfterCloseWriter) Close() error {
	if err := w.RemoteWriter.Close(); err != nil {
		return err
	}
	w.mutate()
	return nil
}

type cancelOnReadFS struct {
	*FileSystem
	cancel context.CancelFunc
	opened bool
}

func (f *cancelOnReadFS) OpenRead(ctx context.Context, path string) (base.RemoteReader, error) {
	reader, err := f.FileSystem.OpenRead(ctx, path)
	if err != nil {
		return nil, err
	}
	return &cancelOnReadReader{RemoteReader: reader, cancel: f.cancel}, nil
}

func (f *cancelOnReadFS) OpenWrite(ctx context.Context, path string, appendMode bool) (base.RemoteWriter, error) {
	f.opened = true
	return f.FileSystem.OpenWrite(ctx, path, appendMode)
}

type cancelOnReadReader struct {
	base.RemoteReader
	cancel context.CancelFunc
}

func (r *cancelOnReadReader) Read(p []byte) (int, error) {
	n, err := r.RemoteReader.Read(p)
	if n > 0 {
		r.cancel()
	}
	return n, err
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
	reader    base.RemoteReader
	finalSize int64
}

func (f *staticReaderFS) OpenRead(context.Context, string) (base.RemoteReader, error) {
	return f.reader, nil
}

func (f *staticReaderFS) List(context.Context, string) ([]base.FileEntry, error) {
	return nil, base.ErrUnsupported
}

func (f *staticReaderFS) Size(context.Context, string) (int64, error) {
	if f.finalSize != 0 {
		return f.finalSize, nil
	}
	return f.reader.Size(), nil
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
