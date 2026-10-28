//go:build !windows

package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func addPrivacyRuntime(t *testing.T, payload, version string) {
	t.Helper()
	directory := filepath.Join(payload, "ai-runtime")
	if err := os.MkdirAll(filepath.Join(directory, "licenses"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"libonnxruntime.so", "runtime.json", "licenses/ONNX-RUNTIME-LICENSE"} {
		if err := os.WriteFile(filepath.Join(directory, file), []byte(version+":"+file), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSystemdPrivacyRuntimeUpgradeRollback(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	v1 := s.payload("v1")
	addPrivacyRuntime(t, v1, "v1")
	s.mustInstall(v1, installOptions{})
	if got := s.read("opt/nexterm/ai-runtime/licenses/ONNX-RUNTIME-LICENSE"); got != "v1:licenses/ONNX-RUNTIME-LICENSE" {
		t.Fatal(got)
	}
	v2 := s.payload("v2")
	addPrivacyRuntime(t, v2, "v2")
	s.mustInstall(v2, installOptions{})
	if got := s.read("opt/nexterm/ai-runtime/libonnxruntime.so"); got != "v2:libonnxruntime.so" {
		t.Fatal(got)
	}
	if got := s.read("opt/nexterm/.rollback/ai-runtime/libonnxruntime.so"); got != "v1:libonnxruntime.so" {
		t.Fatal(got)
	}
	s.mustInstall(v2, installOptions{args: []string{"--rollback"}})
	if got := s.read("opt/nexterm/ai-runtime/libonnxruntime.so"); got != "v1:libonnxruntime.so" {
		t.Fatal(got)
	}
	if got := s.read("opt/nexterm/.rollback.failed/ai-runtime/libonnxruntime.so"); got != "v2:libonnxruntime.so" {
		t.Fatal(got)
	}
}

func TestSystemdRejectsIncompletePrivacyRuntimeBeforeMutation(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	payload := s.payload("v1")
	addPrivacyRuntime(t, payload, "v1")
	if err := os.Remove(filepath.Join(payload, "ai-runtime", "libonnxruntime.so")); err != nil {
		t.Fatal(err)
	}
	result := s.mustFail(payload, installOptions{})
	if !strings.Contains(result.stderr, "ai-runtime/libonnxruntime.so") {
		t.Fatal(result.stderr)
	}
	if s.exists("opt/nexterm/nexterm-server") {
		t.Fatal("mutated installation before validation")
	}
}

func TestSystemdRollbackRemovesRuntimeAbsentFromOldPayload(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.mustInstall(s.payload("v1"), installOptions{})
	v2 := s.payload("v2")
	addPrivacyRuntime(t, v2, "v2")
	s.mustInstall(v2, installOptions{})
	s.mustInstall(v2, installOptions{args: []string{"--rollback"}})
	if s.exists("opt/nexterm/ai-runtime") {
		t.Fatal("retained runtime for reverted binary")
	}
}

func TestSystemdRejectsServerBinaryThatCannotRunBeforeMutation(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	payload := s.payload("v1")
	writeFile(t, filepath.Join(payload, "nexterm-server"), "#!/bin/sh\necho 'version GLIBC_2.39 not found' >&2\nexit 1\n", 0o755)
	result := s.mustFail(payload, installOptions{})
	if !strings.Contains(result.stderr, "cannot run on this host") || !strings.Contains(result.stderr, "GLIBC_2.39") {
		t.Fatal(result.stderr)
	}
	if s.exists("opt/nexterm") || s.exists("etc/nexterm") {
		t.Fatal("mutated installation before the binary preflight")
	}
}
