package production

import (
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func TestCredentialSourceValidationMessages(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	credentialVault := vault.Load(ctx, database)
	dispatcher := ipc.NewDispatcher()
	if err := registerVaultCommands(dispatcher, credentialVault, database); err != nil {
		t.Fatal(err)
	}
	requireProductionNull(t, dispatchStoreTest(dispatcher, "vault_init_master", `{"password":"test-password"}`))

	response := dispatchStoreTest(dispatcher, "vault_set_credential", `{"args":{"name":"deploy key","kind":"private_key","secret":"KEY MATERIAL","source":"bogus"}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("unknown source = %+v, want bad_param", response)
	}
	if response.Error.Message != `参数错误: 未知的凭据来源 "bogus"` {
		t.Fatalf("unknown source message = %q", response.Error.Message)
	}

	var saved map[string]string
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "vault_set_credential", `{"args":{"name":"deploy key","kind":"private_key","secret":"KEY MATERIAL","source":"inline"}}`), &saved)

	response = dispatchStoreTest(dispatcher, "credential_update", `{"args":{"id":"`+saved["id"]+`","source":"file"}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("source change without secret = %+v, want bad_param", response)
	}
	if response.Error.Message != "参数错误: 更改凭据来源需要提供新的密钥内容" {
		t.Fatalf("source change message = %q", response.Error.Message)
	}
}
