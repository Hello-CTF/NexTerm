package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func fileRegistry(files *fakeFS) (*Registry, *fakeTransport) {
	transport := &fakeTransport{files: files}
	registry := NewRegistry(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
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
		Transport: func(context.Context, string) (base.Transport, error) { return transport, nil },
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
