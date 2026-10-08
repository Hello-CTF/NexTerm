package production

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestAssetKeyFileValidationMessages(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	unavailable := ipc.NewDispatcher()
	if err := registerStoreCommands(unavailable, database, nil, "", true); err != nil {
		t.Fatal(err)
	}
	response := dispatchStoreTest(unavailable, "asset_save_key_file", `{"content":"key"}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("asset_save_key_file without data dir = %+v, want unsupported", response)
	}
	if response.Error.Message != "密钥存储不可用" {
		t.Fatalf("asset_save_key_file message = %q", response.Error.Message)
	}

	dispatcher := ipc.NewDispatcher()
	if err := registerStoreCommands(dispatcher, database, nil, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	oversized := `"` + strings.Repeat("a", 64<<10+1) + `"`
	response = dispatchStoreTest(dispatcher, "asset_save_key_file", `{"content":`+oversized+`}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("oversized asset_save_key_file = %+v, want bad_param", response)
	}
	if response.Error.Message != "参数错误: 私钥内容超过 64 KiB 上限" {
		t.Fatalf("asset_save_key_file message = %q", response.Error.Message)
	}
}

func TestAssetReadKeyFileServerAssemblyRejected(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerStoreCommands(dispatcher, database, nil, t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir() + "/server.key"
	if err := os.WriteFile(outside, []byte("SERVER SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	response := dispatchStoreTest(dispatcher, "asset_read_key_file", `{"path":`+strconv.Quote(outside)+`}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("server assembly asset_read_key_file = %+v, want unsupported", response)
	}
	if response.Error.Message != "读取本地密钥文件只在桌面端可用" {
		t.Fatalf("asset_read_key_file message = %q", response.Error.Message)
	}
}

func TestSnippetValidationMessages(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerStoreCommands(dispatcher, database, nil, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}

	response := dispatchStoreTest(dispatcher, "snippet_create", `{"name":"","body":"echo hi"}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("empty snippet name = %+v, want bad_param", response)
	}
	if response.Error.Message != "参数错误: 片段名称不能为空" {
		t.Fatalf("snippet name message = %q", response.Error.Message)
	}

	response = dispatchStoreTest(dispatcher, "snippet_create", `{"name":"看日志","body":" "}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("empty snippet body = %+v, want bad_param", response)
	}
	if response.Error.Message != "参数错误: 片段内容不能为空" {
		t.Fatalf("snippet body message = %q", response.Error.Message)
	}
}
