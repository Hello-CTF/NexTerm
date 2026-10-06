package sync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func dispatchJSON(t *testing.T, dispatcher *ipc.Dispatcher, command string, args string) ipc.Response {
	t.Helper()
	response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
	return response
}

func jsonString(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

func TestBundleFileReadWriteRoundTripThroughIPC(t *testing.T) {
	desktop := newTestInstance(t, true)
	dispatcher := ipc.NewDispatcher()
	if err := desktop.service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}

	content := `{"protocol":2,"objects":[{"id":"obj-1","blob":"AAECAw=="}]}`
	path := filepath.Join(t.TempDir(), "nexterm-assets.json")
	write := dispatchJSON(t, dispatcher, CommandBundleWrite, `{"args":{"path":`+jsonString(path)+`,"content":`+jsonString(content)+`}}`)
	if !write.OK {
		t.Fatalf("bundle write response=%+v", write)
	}
	read := dispatchJSON(t, dispatcher, CommandBundleRead, `{"args":{"path":`+jsonString(path)+`}}`)
	if !read.OK {
		t.Fatalf("bundle read response=%+v", read)
	}
	var text string
	if err := json.Unmarshal(read.Data, &text); err != nil || text != content {
		t.Fatalf("bundle read did not round trip: err=%v", err)
	}

	encryptedPath := filepath.Join(t.TempDir(), "nexterm-assets.enc.json")
	write = dispatchJSON(t, dispatcher, CommandBundleWrite, `{"args":{"path":`+jsonString(encryptedPath)+`,"content":`+jsonString(content)+`,"password":"pw"}}`)
	if !write.OK {
		t.Fatalf("encrypted bundle write response=%+v", write)
	}
	raw, err := os.ReadFile(encryptedPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "obj-1") {
		t.Fatal("encrypted bundle leaks plaintext")
	}
	read = dispatchJSON(t, dispatcher, CommandBundleRead, `{"args":{"path":`+jsonString(encryptedPath)+`,"password":"wrong"}}`)
	if read.OK {
		t.Fatalf("wrong password accepted: %+v", read)
	}
	read = dispatchJSON(t, dispatcher, CommandBundleRead, `{"args":{"path":`+jsonString(encryptedPath)+`,"password":"pw"}}`)
	if !read.OK {
		t.Fatalf("encrypted bundle read response=%+v", read)
	}
	if err := json.Unmarshal(read.Data, &text); err != nil || text != content {
		t.Fatalf("encrypted bundle did not round trip: err=%v", err)
	}
}

func TestBundleFileCommandsValidateInputAndRequireDesktop(t *testing.T) {
	server := newTestInstance(t, true)
	serverService := New(server.db, server.vault)
	serverDispatcher := ipc.NewDispatcher()
	if err := serverService.RegisterCommands(serverDispatcher); err != nil {
		t.Fatal(err)
	}
	response := dispatchJSON(t, serverDispatcher, CommandBundleRead, `{"args":{"path":"/tmp/x.json"}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("server bundle read response=%+v", response)
	}
	response = dispatchJSON(t, serverDispatcher, CommandBundleWrite, `{"args":{"path":"/tmp/x.json","content":"{}"}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("server bundle write response=%+v", response)
	}

	desktop := newTestInstance(t, true)
	dispatcher := ipc.NewDispatcher()
	if err := desktop.service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	response = dispatchJSON(t, dispatcher, CommandBundleRead, `{"args":{"path":"  "}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("empty path response=%+v", response)
	}
	response = dispatchJSON(t, dispatcher, CommandBundleRead, `{"args":{"path":`+jsonString(filepath.Join(t.TempDir(), "missing.json"))+`}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("missing file response=%+v", response)
	}

	path := filepath.Join(t.TempDir(), "bundle.json")
	oversize := strings.Repeat("x", maxBundleFileBytes+1)
	response = dispatchJSON(t, dispatcher, CommandBundleWrite, `{"args":{"path":`+jsonString(path)+`,"content":`+jsonString(oversize)+`}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("oversize write response=%+v", response)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("oversize write created a file: %v", err)
	}
}
