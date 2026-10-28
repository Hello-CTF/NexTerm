//go:build windows

package update

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// loadRuntimeDLL copies a real system DLL to ai-runtime/onnxruntime.dll and
// maps it, reproducing the running desktop's hold on ONNX Runtime.
func loadRuntimeDLL(t *testing.T, dir string) (string, []byte, *syscall.DLL) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(os.Getenv("SystemRoot"), "System32", "version.dll"))
	if err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(dir, "ai-runtime", "onnxruntime.dll")
	writePayloadFile(t, library, string(data))
	dll, err := syscall.LoadDLL(library)
	if err != nil {
		t.Fatal(err)
	}
	// Whether Windows also refuses to rename the containing directory varies by
	// release, so the test models only what the update relies on: the mapped
	// image can be neither deleted nor overwritten in place.
	if err := os.Remove(library); err == nil {
		_ = dll.Release()
		t.Fatal("Windows deleted a loaded DLL; this test no longer models the update constraint")
	}
	if err := os.WriteFile(library, []byte("overwrite"), 0o755); err == nil {
		_ = dll.Release()
		t.Fatal("Windows overwrote a loaded DLL; this test no longer models the update constraint")
	}
	return library, data, dll
}

func TestWindowsPayloadWithLoadedRuntimeDLL(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "nexterm.exe")
		writePayloadFile(t, exe, "old")
		library, _, dll := loadRuntimeDLL(t, dir)
		released := false
		defer func() {
			if !released {
				_ = dll.Release()
			}
		}()
		if err := installWindowsPayload(exe, func() error {
			writePayloadFile(t, exe, "new")
			writePayloadFile(t, library, "new-ort")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		requirePayloadFile(t, exe, "new")
		requirePayloadFile(t, library, "new-ort")
		backups, err := filepath.Glob(filepath.Join(dir, windowsBackupPrefix+"*"))
		if err != nil || len(backups) != 1 {
			t.Fatalf("expected the mapped image to stay in one backup until unloaded: %v %v", backups, err)
		}
		if err := dll.Release(); err != nil {
			t.Fatal(err)
		}
		released = true
		if err := installWindowsPayload(exe, func() error {
			writePayloadFile(t, exe, "newer")
			writePayloadFile(t, library, "newer-ort")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		requirePayloadFile(t, library, "newer-ort")
		requireNoWindowsBackups(t, dir)
	})
	t.Run("installer error", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "nexterm.exe")
		writePayloadFile(t, exe, "old")
		library, original, dll := loadRuntimeDLL(t, dir)
		defer func() { _ = dll.Release() }()
		err := installWindowsPayload(exe, func() error {
			writePayloadFile(t, exe, "new")
			writePayloadFile(t, library, "partial")
			return errors.New("failed installer")
		})
		if err == nil {
			t.Fatal("expected failure")
		}
		requirePayloadFile(t, exe, "old")
		requirePayloadFile(t, library, string(original))
		requireNoWindowsBackups(t, dir)
	})
}
