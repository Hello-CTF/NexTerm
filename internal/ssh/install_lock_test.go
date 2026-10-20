//go:build unix

package ssh

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

type installLockHost struct {
	Host
	lock            string
	mkdirErr        error
	releaseOnExists bool
	mkdirCalls      int
	existsCalls     int
}

func (h *installLockHost) Mkdir(ctx context.Context, path string) error {
	if path != h.lock {
		return h.Host.Mkdir(ctx, path)
	}
	h.mkdirCalls++
	if h.mkdirErr != nil && h.mkdirCalls == 1 {
		return h.mkdirErr
	}
	if h.mkdirCalls > 1 {
		return h.Host.Mkdir(ctx, path)
	}
	if err := h.Host.Mkdir(ctx, path); err != nil {
		return err
	}
	return fs.ErrExist
}

func (h *installLockHost) Exists(ctx context.Context, path string) (bool, error) {
	if path == h.lock {
		h.existsCalls++
		if h.releaseOnExists && h.existsCalls == 1 {
			if err := h.Host.Delete(ctx, path, true); err != nil {
				return false, err
			}
		}
	}
	return h.Host.Exists(ctx, path)
}

func TestInstallRetriesWhenContendedLockDisappears(t *testing.T) {
	digest := strings.Repeat("78", 32)
	shared := newSharedTarget("")
	binDir := filepath.Join(t.TempDir(), "bin")
	lock := binDir + "/.nexterm-" + digest + ".install"
	host := &installLockHost{
		Host:            &fakeHost{shared: shared, uploadDigest: digest},
		lock:            lock,
		releaseOnExists: true,
	}
	upload := fakeUploadSource(digest)
	target, err := (&Resolver{}).install(context.Background(), host, binDir, &upload, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := binDir + "/nexterm-" + digest[:12]; target != want {
		t.Fatalf("target = %q, want %q", target, want)
	}
	if host.mkdirCalls != 2 || host.existsCalls != 1 {
		t.Fatalf("lock calls = %d mkdir/%d exists, want 2/1", host.mkdirCalls, host.existsCalls)
	}
	if shared.exists(lock) {
		t.Fatal("installation lock remained after cleanup")
	}
}

func TestInstallReturnsNonExistLockErrorWithoutRetry(t *testing.T) {
	digest := strings.Repeat("90", 32)
	shared := newSharedTarget("")
	binDir := filepath.Join(t.TempDir(), "bin")
	lock := binDir + "/.nexterm-" + digest + ".install"
	mkdirErr := errors.New("permission denied")
	host := &installLockHost{
		Host:     &fakeHost{shared: shared, uploadDigest: digest},
		lock:     lock,
		mkdirErr: mkdirErr,
	}
	upload := fakeUploadSource(digest)
	_, err := (&Resolver{}).install(context.Background(), host, binDir, &upload, false)
	if err == nil || !strings.Contains(err.Error(), mkdirErr.Error()) {
		t.Fatalf("install error = %v, want %q", err, mkdirErr)
	}
	if host.mkdirCalls != 1 || host.existsCalls != 0 {
		t.Fatalf("lock calls = %d mkdir/%d exists, want 1/0", host.mkdirCalls, host.existsCalls)
	}
	if uploads, _, _ := shared.counts(); uploads != 0 {
		t.Fatalf("uploads = %d after lock failure, want 0", uploads)
	}
}
