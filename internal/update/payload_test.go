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
	valid := []payloadEntry{{"usr/bin/nexterm-desktop", "new", 0}, {"usr/bin/ai-runtime/libnexterm_privacy.so", "native", 0}, {"usr/bin/ai-runtime/libonnxruntime.so", "ort", 0}}
	for _, test := range []struct {
		name         string
		entries      []payloadEntry
		cancel, fail bool
	}{
		{name: "complete", entries: valid}, {name: "legacy", entries: valid[:1]},
		{name: "incomplete", entries: valid[:2], fail: true},
		{name: "duplicate", entries: append(append([]payloadEntry{}, valid...), valid[1]), fail: true},
		{name: "traversal", entries: append(append([]payloadEntry{}, valid...), payloadEntry{"usr/bin/ai-runtime/../../outside", "bad", 0}), fail: true},
		{name: "symlink", entries: append(append([]payloadEntry{}, valid...), payloadEntry{"usr/bin/ai-runtime/link", "", tar.TypeSymlink}), fail: true},
		{name: "directory instead of library", entries: []payloadEntry{valid[0], valid[1], {"usr/bin/ai-runtime/libonnxruntime.so/child", "bad", 0}}, fail: true},
		{name: "canceled", entries: valid, cancel: true, fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "nexterm-desktop")
			runtime := filepath.Join(dir, "ai-runtime/libnexterm_privacy.so")
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
			} else {
				if err != nil {
					t.Fatal(err)
				}
				requirePayloadFile(t, exe, "new")
				if len(test.entries) > 1 {
					requirePayloadFile(t, runtime, "native")
				} else {
					requirePayloadFile(t, runtime, "old-native")
				}
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
	library := filepath.Join(dir, "ai-runtime/libnexterm_privacy.so")
	writePayloadFile(t, library, "old")
	err := installLinuxPayload(context.Background(), payloadTar(t, payloadEntry{"usr/bin/nexterm-desktop", "new", 0}, payloadEntry{"usr/bin/ai-runtime/libnexterm_privacy.so", "native", 0}, payloadEntry{"usr/bin/ai-runtime/libonnxruntime.so", "ort", 0}), exe)
	if err == nil {
		t.Fatal("expected executable replacement failure")
	}
	requirePayloadFile(t, library, "old")
}

func TestWindowsPayloadRollsBackBothImages(t *testing.T) {
	for _, mode := range []string{"success", "installer error", "incomplete runtime", "no old runtime"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "nexterm.exe")
			native := filepath.Join(dir, "ai-runtime/nexterm_privacy.dll")
			writePayloadFile(t, exe, "old")
			if mode != "no old runtime" {
				writePayloadFile(t, native, "old-native")
			}
			err := installWindowsPayload(exe, func() error {
				if _, err := os.Stat(exe); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("loaded executable not moved aside")
				}
				if _, err := os.Stat(filepath.Dir(native)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("loaded runtime not moved aside")
				}
				writePayloadFile(t, exe, "new")
				writePayloadFile(t, native, "new-native")
				if mode == "installer error" || mode == "no old runtime" {
					return errors.New("failed installer")
				}
				if mode != "incomplete runtime" {
					writePayloadFile(t, filepath.Join(dir, "ai-runtime/onnxruntime.dll"), "ort")
				}
				return nil
			})
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				requirePayloadFile(t, exe, "new")
				requirePayloadFile(t, native, "new-native")
			} else {
				if err == nil {
					t.Fatal("expected failure")
				}
				requirePayloadFile(t, exe, "old")
				if mode != "no old runtime" {
					requirePayloadFile(t, native, "old-native")
				} else if _, err := os.Stat(filepath.Dir(native)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("partial runtime remains: %v", err)
				}
			}
		})
	}
}
