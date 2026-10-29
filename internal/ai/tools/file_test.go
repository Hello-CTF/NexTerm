package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// fileRegistry builds a registry whose asset has the pre-overwrite backup enabled.
func fileRegistry(files *fakeFS) (*Registry, *fakeTransport) {
	return fileRegistryBackup(files, func(context.Context, Scope) bool { return true })
}

func fileRegistryBackup(files *fakeFS, backup func(context.Context, Scope) bool) (*Registry, *fakeTransport) {
	transport := &fakeTransport{files: files}
	registry := NewRegistry(Dependencies{
		Transport:             func(context.Context, string) (base.Transport, error) { return transport, nil },
		BackupBeforeOverwrite: backup,
	})
	return registry, transport
}

func executeTool(registry *Registry, job, name, args string) Output {
	return registry.Execute(context.Background(), job, Scope{SessionID: "s"}, Call{ID: name, Name: name, Args: json.RawMessage(args)}, nil)
}

func TestReadBeforeWriteAndVersionCheck(t *testing.T) {
	files := newFakeFS()
	files.files["/a"] = []byte("old")
	transport := &fakeTransport{files: files}
	var audits []AuditEntry
	registry := NewRegistry(Dependencies{
		Transport:             func(context.Context, string) (base.Transport, error) { return transport, nil },
		BackupBeforeOverwrite: func(context.Context, Scope) bool { return true },
		Audit: func(_ context.Context, entry AuditEntry) error {
			audits = append(audits, entry)
			return nil
		},
	})
	write := Call{ID: "w", Name: "write_file", Args: json.RawMessage(`{"path":"/a","content":"new"}`)}
	if _, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, write); !errors.Is(err, ErrReadRequired) {
		t.Fatalf("write without read: %v", err)
	}
	if result := executeTool(registry, "j", "read_file", `{"path":"/a"}`); !result.OK {
		t.Fatal(result.Text)
	}
	preparation, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, write)
	if err != nil || preparation.Preview == nil || preparation.Preview.Kind != "modify" {
		t.Fatalf("prepare=%+v err=%v", preparation, err)
	}
	files.files["/a"] = []byte("external")
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, write, preparation)
	if !result.OK || !strings.Contains(result.Text, "非事务覆盖") {
		t.Fatalf("result=%+v", result)
	}
	if string(files.files["/a"]) != "new" || string(files.files["/a.nexterm-bak"]) != "external" {
		t.Fatalf("non-CAS overwrite content=%q backup=%q", files.files["/a"], files.files["/a.nexterm-bak"])
	}
	verified := false
	for _, entry := range audits {
		if entry.Kind != "write_file" {
			continue
		}
		payload, ok := entry.Payload.(map[string]any)
		verified = ok && payload["writeSemantics"] == writeReplaceSemantics && payload["outcome"] == "committed"
	}
	if !verified {
		t.Fatalf("missing non-CAS audit semantics: %+v", audits)
	}
}

func TestFailedReadDoesNotAuthorizeOverwrite(t *testing.T) {
	files := newFakeFS()
	files.files["/a"] = []byte("old")
	files.readErr = errors.New("permission denied")
	registry, _ := fileRegistry(files)
	if result := executeTool(registry, "j", "read_file", `{"path":"/a"}`); result.OK {
		t.Fatal("read unexpectedly succeeded")
	}
	write := Call{ID: "w", Name: "write_file", Args: json.RawMessage(`{"path":"/a","content":"new"}`)}
	if _, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, write); !errors.Is(err, ErrReadRequired) {
		t.Fatalf("failed read authorized write: %v", err)
	}
}

func TestCreateDiffAndFailedWriteHasNoChange(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		files := newFakeFS()
		registry, _ := fileRegistry(files)
		if result := executeTool(registry, "j", "read_file", `{"path":"/new"}`); result.OK {
			t.Fatal("missing read succeeded")
		}
		write := Call{ID: "w", Name: "write_file", Args: json.RawMessage(`{"path":"/new","content":"content"}`)}
		preparation, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, write)
		if err != nil || preparation.Preview == nil || preparation.Preview.Kind != "create" {
			t.Fatalf("preparation=%+v err=%v", preparation, err)
		}
		result := registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, write, preparation)
		if !result.OK || result.Change == nil || result.Change.Before != "" || result.Change.After != "content" {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("failed write", func(t *testing.T) {
		files := newFakeFS()
		files.files["/a"] = []byte("old")
		registry, _ := fileRegistry(files)
		executeTool(registry, "j", "read_file", `{"path":"/a"}`)
		write := Call{ID: "w", Name: "write_file", Args: json.RawMessage(`{"path":"/a","content":"new"}`)}
		preparation, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, write)
		if err != nil {
			t.Fatal(err)
		}
		files.writeErr = errors.New("backup failed")
		result := registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, write, preparation)
		if result.OK || result.Change != nil || string(files.files["/a"]) != "old" {
			t.Fatalf("result=%+v content=%q", result, files.files["/a"])
		}
	})
}

func TestEditMatchingAndBackup(t *testing.T) {
	files := newFakeFS()
	files.files["/a"] = []byte("a a")
	registry, _ := fileRegistry(files)
	executeTool(registry, "j", "read_file", `{"path":"/a"}`)
	edit := Call{ID: "e", Name: "edit_file", Args: json.RawMessage(`{"path":"/a","old_string":"a","new_string":"b"}`)}
	if _, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, edit); err == nil {
		t.Fatal("non-unique edit accepted")
	}
	edit.Args = json.RawMessage(`{"path":"/a","old_string":"a","new_string":"b","replace_all":true}`)
	preparation, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, edit)
	if err != nil {
		t.Fatal(err)
	}
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, edit, preparation)
	if !result.OK || string(files.files["/a"]) != "b b" || string(files.files["/a.nexterm-bak"]) != "a a" || result.Change == nil {
		t.Fatalf("result=%+v content=%q backup=%q", result, files.files["/a"], files.files["/a.nexterm-bak"])
	}
}

func overwriteWith(t *testing.T, registry *Registry, files *fakeFS) Output {
	t.Helper()
	files.files["/a"] = []byte("old")
	if result := executeTool(registry, "j", "read_file", `{"path":"/a"}`); !result.OK {
		t.Fatal(result.Text)
	}
	write := Call{ID: "w", Name: "write_file", Args: json.RawMessage(`{"path":"/a","content":"new"}`)}
	preparation, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, write)
	if err != nil {
		t.Fatal(err)
	}
	return registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, write, preparation)
}

func TestOverwriteBackupIsOffByDefaultAndReportedHonestly(t *testing.T) {
	for name, backup := range map[string]func(context.Context, Scope) bool{
		"unconfigured": nil,
		"disabled":     func(context.Context, Scope) bool { return false },
	} {
		t.Run(name, func(t *testing.T) {
			files := newFakeFS()
			registry, _ := fileRegistryBackup(files, backup)
			result := overwriteWith(t, registry, files)
			if !result.OK || string(files.files["/a"]) != "new" {
				t.Fatalf("result=%+v", result)
			}
			if _, exists := files.files["/a.nexterm-bak"]; exists {
				t.Fatal("backup written although the asset did not enable it")
			}
			if !strings.Contains(result.Text, "未备份原文件") || strings.Contains(result.Text, "已备份") {
				t.Fatalf("result claims a backup: %q", result.Text)
			}
		})
	}
}

func TestOverwriteBackupFollowsCurrentSettingWithoutReconnect(t *testing.T) {
	files := newFakeFS()
	enabled := false
	registry, _ := fileRegistryBackup(files, func(context.Context, Scope) bool { return enabled })
	if result := overwriteWith(t, registry, files); !result.OK || strings.Contains(result.Text, "已备份") {
		t.Fatalf("first result=%+v", result)
	}
	if _, exists := files.files["/a.nexterm-bak"]; exists {
		t.Fatal("backup written while disabled")
	}
	enabled = true
	files2 := newFakeFS()
	registry2, _ := fileRegistryBackup(files2, func(context.Context, Scope) bool { return enabled })
	result := overwriteWith(t, registry2, files2)
	if !result.OK || !strings.Contains(result.Text, "已备份原文件") || string(files2.files["/a.nexterm-bak"]) != "old" {
		t.Fatalf("enabled result=%+v backup=%q", result, files2.files["/a.nexterm-bak"])
	}
}
