package mount

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestRealMountInterop(t *testing.T) {
	if os.Getenv("NEXTERM_MOUNT_INTEROP") != "1" {
		t.Skip("opt-in real OS mount skipped: set NEXTERM_MOUNT_INTEROP=1 plus NEXTERM_MOUNT_TEST_REMOTE and NEXTERM_MOUNT_TEST_POINT; Windows also requires USERNAME/PASSWORD to exercise ConPTY credentials")
	}
	if runtime.GOOS == "darwin" {
		t.Skip("real mount is explicitly unsupported on macOS")
	}
	remote := os.Getenv("NEXTERM_MOUNT_TEST_REMOTE")
	point := os.Getenv("NEXTERM_MOUNT_TEST_POINT")
	if remote == "" || point == "" {
		t.Fatal("NEXTERM_MOUNT_TEST_REMOTE and NEXTERM_MOUNT_TEST_POINT are required")
	}
	args := CreateArgs{
		SessionID:  "real-mount",
		RemotePath: remote,
		LocalPoint: point,
		Username:   os.Getenv("NEXTERM_MOUNT_TEST_USERNAME"),
		Password:   os.Getenv("NEXTERM_MOUNT_TEST_PASSWORD"),
	}
	if runtime.GOOS == "windows" && args.Username == "" {
		t.Skip("real Windows credential mount skipped: set NEXTERM_MOUNT_TEST_USERNAME and NEXTERM_MOUNT_TEST_PASSWORD")
	}
	service := NewService(Config{})
	existing, err := service.List(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range existing {
		if strings.EqualFold(entry.LocalPoint, point) {
			t.Fatalf("refusing to replace existing mount at %q", point)
		}
	}
	if runtime.GOOS == "linux" {
		info, err := os.Stat(point)
		if err != nil || !info.IsDir() {
			t.Fatalf("Linux mount point must be an existing directory: %v", err)
		}
		entries, err := os.ReadDir(point)
		if err != nil || len(entries) != 0 {
			t.Fatalf("Linux mount point must be empty (entries=%d): %v", len(entries), err)
		}
	}
	if runtime.GOOS == "windows" {
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		_, cancelErr := service.Create(canceled, args)
		if cancelErr == nil || !errors.Is(cancelErr, context.Canceled) {
			_ = service.Remove(context.Background(), RemoveArgs{LocalPoint: point, SessionID: "real-mount"})
			t.Fatalf("already-canceled mount error = %v; any unexpected mount was removed", cancelErr)
		}
	}
	created, err := service.Create(t.Context(), args)
	if err != nil {
		t.Fatal(err)
	}
	mounted := false
	defer func() {
		if mounted {
			if err := service.Remove(t.Context(), RemoveArgs{LocalPoint: point, SessionID: "real-mount"}); err != nil {
				t.Errorf("cleanup unmount: %v", err)
			}
		}
	}()
	mounted = true
	entries, err := service.List(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if strings.EqualFold(entry.LocalPoint, created.LocalPoint) {
			found = true
		}
	}
	if !found {
		t.Fatalf("created mount %q not present in real mount table: %+v", point, entries)
	}
	if err := service.Remove(t.Context(), RemoveArgs{LocalPoint: point, SessionID: "real-mount"}); err != nil {
		t.Fatal(err)
	}
	entries, err = service.List(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.LocalPoint, point) {
			t.Fatalf("mount %q remains in the real mount table after removal", point)
		}
	}
	mounted = false
}
