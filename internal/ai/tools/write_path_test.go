package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

func TestNormalizeWritePathArgs(t *testing.T) {
	t.Parallel()
	clean, _ := json.Marshal(map[string]string{"path": filepath.Clean("/a/b"), "content": "x"})
	got, err := normalizeWritePathArgs(clean)
	if err != nil || string(got) != string(clean) {
		t.Fatalf("clean args changed: %s err=%v", got, err)
	}
	unclean := json.RawMessage(`{"path":"/a/./b/../c","content":"x","old_string":"o","new_string":"n","replace_all":true}`)
	got, err = normalizeWritePathArgs(unclean)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(got, &fields); err != nil {
		t.Fatal(err)
	}
	var path string
	if want := filepath.Clean("/a/c"); json.Unmarshal(fields["path"], &path) != nil || path != want {
		t.Fatalf("normalized path = %q, want %q", path, want)
	}
	if len(fields) != 5 {
		t.Fatalf("fields dropped: %s", got)
	}
	if _, err := normalizeWritePathArgs(json.RawMessage(`{"path":"../escape","content":"x"}`)); err == nil {
		t.Fatal("escaping path accepted")
	}
	passthrough := json.RawMessage(`{"path":123}`)
	if got, err := normalizeWritePathArgs(passthrough); err != nil || string(got) != string(passthrough) {
		t.Fatalf("non-string path not passed through: %s err=%v", got, err)
	}
}

func TestNormalizeWritePathArgsPreservesUnixBackslash(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Unix path semantics")
	}
	args, _ := json.Marshal(map[string]string{"path": `/tmp/a\b`, "content": "x"})
	got, err := normalizeWritePathArgs(args)
	if err != nil || string(got) != string(args) {
		t.Fatalf("Unix backslash path changed: got %s, want %s, err=%v", got, args, err)
	}
}

func TestNormalizeWritePathArgsPreservesWindowsUNC(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("Windows path semantics")
	}
	args, _ := json.Marshal(map[string]string{"path": `\\server\share\dir\file.txt`, "content": "x"})
	got, err := normalizeWritePathArgs(args)
	if err != nil || string(got) != string(args) {
		t.Fatalf("UNC path changed: got %s, want %s, err=%v", got, args, err)
	}
}

func TestNormalizeWritePathArgsRejectsWindowsDriveRelativeParent(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("Windows path semantics")
	}
	args, _ := json.Marshal(map[string]string{"path": `C:..\escape`, "content": "x"})
	if _, err := normalizeWritePathArgs(args); err == nil {
		t.Fatal("drive-relative parent escape accepted")
	}
}

func TestWritePathNormalizedBeforeExecution(t *testing.T) {
	h := newChatGrantHarness(guard.Config{Mode: guard.Silent}, "asset-a", nil, &chatGrantRecorder{})
	target := filepath.Clean("/tmp/new.txt")
	readArgs, _ := json.Marshal(map[string]string{"path": target})
	if _, err := h.invoke("call-1", "read_file", string(readArgs)); err != nil {
		t.Fatalf("read setup: %v", err)
	}
	writeArgs, _ := json.Marshal(map[string]string{"path": "/tmp/dir/../new.txt", "content": "hi"})
	messages, err := h.invoke("call-2", "write_file", string(writeArgs))
	requireChatGrantResult(t, messages, err, "已创建 ")
	h.transport.files.mu.Lock()
	_, normalized := h.transport.files.files[target]
	_, raw := h.transport.files.files["/tmp/dir/../new.txt"]
	h.transport.files.mu.Unlock()
	if !normalized || raw {
		t.Fatalf("write did not land at normalized path: normalized=%v raw=%v", normalized, raw)
	}
}

func TestWritePathRejectsParentEscape(t *testing.T) {
	h := newChatGrantHarness(guard.Config{Mode: guard.Silent}, "asset-a", nil, &chatGrantRecorder{})
	messages, err := h.invoke("call-1", "write_file", `{"path":"../escape.txt","content":"hi"}`)
	requireChatGrantResult(t, messages, err, "路径越界")
	messages, err = h.invoke("call-2", "edit_file", `{"path":"a/../../escape.txt","old_string":"x","new_string":"y"}`)
	requireChatGrantResult(t, messages, err, "路径越界")
	h.transport.files.mu.Lock()
	writes := h.transport.files.writeCalls
	h.transport.files.mu.Unlock()
	if writes != 0 {
		t.Fatalf("escaping writes executed: %d", writes)
	}
}

func TestPersistentRuleDoesNotCoverParentTraversal(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.AddRule(context.Background(), "asset-a", "write_file", "/var/log/**", time.Time{}); err != nil {
		t.Fatal(err)
	}
	h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, recorder)
	if _, err := h.invoke("call-1", "read_file", `{"path":"/var/etc/cron.d/x"}`); err != nil {
		t.Fatalf("read setup: %v", err)
	}
	_, err := h.invoke("call-2", "write_file", `{"path":"/var/log/../etc/cron.d/x","content":"hi"}`)
	requireChatGrantInterrupt(t, err)
	h.transport.files.mu.Lock()
	writes := h.transport.files.writeCalls
	h.transport.files.mu.Unlock()
	if writes != 0 {
		t.Fatal("traversal write executed under rule")
	}
}

func TestPersistentRuleCoversNormalizedInTreePath(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.AddRule(context.Background(), "asset-a", "write_file", "/var/log/**", time.Time{}); err != nil {
		t.Fatal(err)
	}
	h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, recorder)
	target := filepath.Clean("/var/log/app.log")
	readArgs, _ := json.Marshal(map[string]string{"path": target})
	if _, err := h.invoke("call-1", "read_file", string(readArgs)); err != nil {
		t.Fatalf("read setup: %v", err)
	}
	writeArgs, _ := json.Marshal(map[string]string{"path": "/var/log/../log/app.log", "content": "hi"})
	messages, err := h.invoke("call-2", "write_file", string(writeArgs))
	requireChatGrantResult(t, messages, err, "已创建 ")
	h.transport.files.mu.Lock()
	content := string(h.transport.files.files[target])
	h.transport.files.mu.Unlock()
	if !strings.Contains(content, "hi") {
		t.Fatalf("normalized in-tree write missing: %q", content)
	}
}
