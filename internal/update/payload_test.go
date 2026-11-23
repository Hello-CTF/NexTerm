package update

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type payloadEntry struct {
	name, body string
	kind       byte
}

func payloadTar(t *testing.T, entries ...payloadEntry) *tar.Reader {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		size := int64(len(e.body))
		if kind != tar.TypeReg {
			size = 0
		}
		if err := w.WriteHeader(&tar.Header{Name: e.name, Mode: 0755, Size: size, Typeflag: kind}); err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if _, err := w.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return tar.NewReader(&b)
}
func writePayloadFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
}
func requirePayloadFile(t *testing.T, path, body string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != body {
		t.Fatalf("%s: %q, %v", path, b, err)
	}
}

func TestLinuxPayloadStagesAndRejectsInvalidRuntime(t *testing.T) {
	marker := `{"unavailable":"the privacy engine is built into the application"}`
	valid := []payloadEntry{{"usr/bin/nexterm-desktop", "new", 0}, {"usr/bin/ai-runtime/runtime.json", marker, 0}}
	for _, test := range []struct {
		name         string
		entries      []payloadEntry
		cancel, fail bool
	}{
		{name: "legacy runtime marker", entries: valid},
		{name: "payload without runtime", entries: valid[:1]},
		{name: "duplicate", entries: append(append([]payloadEntry{}, valid...), valid[1]), fail: true},
		{name: "traversal", entries: append(append([]payloadEntry{}, valid...), payloadEntry{"usr/bin/ai-runtime/../../outside", "bad", 0}), fail: true},
		{name: "symlink", entries: append(append([]payloadEntry{}, valid...), payloadEntry{"usr/bin/ai-runtime/link", "", tar.TypeSymlink}), fail: true},
		{name: "canceled", entries: valid, cancel: true, fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "nexterm-desktop")
			runtime := filepath.Join(dir, "ai-runtime/libonnxruntime.so")
			writePayloadFile(t, exe, "old")
			writePayloadFile(t, runtime, "old-native")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancel {
				cancel()
			}
			err := installLinuxPayload(ctx, payloadTar(t, test.entries...), exe)
			if test.fail {
				if err == nil {
					t.Fatal("expected failure")
				}
				requirePayloadFile(t, exe, "old")
				requirePayloadFile(t, runtime, "old-native")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			requirePayloadFile(t, exe, "new")
			if len(test.entries) == 1 {
				// A payload without ai-runtime leaves the existing directory alone.
				requirePayloadFile(t, runtime, "old-native")
				return
			}
			// The marker replaces the ONNX Runtime an older release installed.
			requirePayloadFile(t, filepath.Join(dir, "ai-runtime/runtime.json"), marker)
			if _, err := os.Stat(runtime); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stale library survived the runtime marker: %v", err)
			}
		})
	}
}

func TestLinuxPayloadRestoresRuntimeWhenExecutableCannotBeReplaced(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "nexterm-desktop")
	if err := os.Mkdir(exe, 0755); err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(dir, "ai-runtime/libonnxruntime.so")
	writePayloadFile(t, library, "old")
	err := installLinuxPayload(context.Background(), payloadTar(t, payloadEntry{"usr/bin/nexterm-desktop", "new", 0}, payloadEntry{"usr/bin/ai-runtime/runtime.json", `{"unavailable":"built in"}`, 0}), exe)
	if err == nil {
		t.Fatal("expected executable replacement failure")
	}
	requirePayloadFile(t, library, "old")
}

func TestWindowsPayloadRollsBackBothImages(t *testing.T) {
	const marker = `{"unavailable":"the privacy engine is built into the application"}`
	for _, mode := range []string{"success", "installer error", "missing executable", "no old runtime"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "nexterm.exe")
			library := filepath.Join(dir, "ai-runtime/onnxruntime.dll")
			license := filepath.Join(dir, "ai-runtime/licenses/ONNX-RUNTIME-LICENSE")
			manifest := filepath.Join(dir, "ai-runtime/runtime.json")
			writePayloadFile(t, exe, "old")
			if mode != "no old runtime" {
				writePayloadFile(t, library, "old-ort")
				writePayloadFile(t, license, "old-license")
			}
			err := installWindowsPayload(exe, func() error {
				for _, name := range []string{exe, library, license} {
					if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("loaded image %s not moved aside", name)
					}
				}
				writePayloadFile(t, manifest, marker)
				if mode == "missing executable" {
					return nil
				}
				writePayloadFile(t, exe, "new")
				if mode == "installer error" || mode == "no old runtime" {
					return errors.New("failed installer")
				}
				return nil
			})
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				requirePayloadFile(t, exe, "new")
				requirePayloadFile(t, manifest, marker)
				for _, name := range []string{library, license} {
					if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("old runtime file %s survived: %v", name, err)
					}
				}
			} else {
				if err == nil {
					t.Fatal("expected failure")
				}
				requirePayloadFile(t, exe, "old")
				if mode != "no old runtime" {
					requirePayloadFile(t, library, "old-ort")
					requirePayloadFile(t, license, "old-license")
				} else if _, err := os.Stat(filepath.Dir(library)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("partial runtime remains: %v", err)
				}
			}
			if mode != "success" {
				requireNoWindowsBackups(t, dir)
			}
		})
	}
}

func TestWindowsPayloadSweepsStaleBackups(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "nexterm.exe")
	writePayloadFile(t, exe, "old")
	writePayloadFile(t, filepath.Join(dir, windowsBackupPrefix+"stale", "ai-runtime", "onnxruntime.dll"), "stale")
	writePayloadFile(t, filepath.Join(dir, "unrelated", "keep"), "keep")
	if err := installWindowsPayload(exe, func() error {
		writePayloadFile(t, exe, "new")
		writePayloadFile(t, filepath.Join(dir, "ai-runtime/runtime.json"), `{"unavailable":"built in"}`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	requireNoWindowsBackups(t, dir)
	requirePayloadFile(t, filepath.Join(dir, "unrelated", "keep"), "keep")
}

func requireNoWindowsBackups(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, windowsBackupPrefix+"*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("stale update backups remain: %v %v", matches, err)
	}
}

func TestWindowsPayloadDoesNotRequireRuntime(t *testing.T) {
	for _, withMarker := range []bool{true, false} {
		dir := t.TempDir()
		exe := filepath.Join(dir, "nexterm.exe")
		writePayloadFile(t, exe, "old")
		err := installWindowsPayload(exe, func() error {
			writePayloadFile(t, exe, "new")
			if withMarker {
				writePayloadFile(t, filepath.Join(dir, "ai-runtime/runtime.json"), `{"unavailable":"built in"}`)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("payload (marker %v) rejected: %v", withMarker, err)
		}
		requirePayloadFile(t, exe, "new")
	}
}
