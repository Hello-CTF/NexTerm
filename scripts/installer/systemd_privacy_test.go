//go:build !windows

package installer

import (
	"testing"
)

// Releases before 1.4.6-rc.4 installed ONNX Runtime in ai-runtime/. The privacy
// engine is now built into the server, so an upgrade removes that directory and
// a rollback restores it together with the old binary.
func TestSystemdUpgradeRemovesLegacyPrivacyRuntime(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.mustInstall(s.payload("v1"), installOptions{})
	s.write("opt/nexterm/ai-runtime/libonnxruntime.so", "v1:libonnxruntime.so")
	s.write("opt/nexterm/ai-runtime/runtime.json", "v1:runtime.json")
	v2 := s.payload("v2")
	s.mustInstall(v2, installOptions{})
	if s.exists("opt/nexterm/ai-runtime") {
		t.Fatal("upgrade kept the legacy ONNX Runtime")
	}
	if got := s.read("opt/nexterm/.rollback/ai-runtime/libonnxruntime.so"); got != "v1:libonnxruntime.so" {
		t.Fatal(got)
	}
	s.mustInstall(v2, installOptions{args: []string{"--rollback"}})
	if got := s.read("opt/nexterm/ai-runtime/libonnxruntime.so"); got != "v1:libonnxruntime.so" {
		t.Fatal(got)
	}
}

// A payload directory left over from an older archive is never installed.
func TestSystemdIgnoresPayloadPrivacyRuntime(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	payload := s.payload("v1")
	writeFile(t, payload+"/ai-runtime/libonnxruntime.so", "stale", 0o644)
	s.mustInstall(payload, installOptions{})
	if s.exists("opt/nexterm/ai-runtime") {
		t.Fatal("installed a payload ai-runtime directory")
	}
}
