package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/privacy"
	"github.com/Hello-CTF/NexTerm/internal/ai/privacy/inference"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestProductionPrivacyCommandsAreOwnerScopedAndDefaultOff(t *testing.T) {
	production, err := NewProduction(t.Context(), ProductionConfig{DataDir: t.TempDir(), Desktop: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	commands := map[string]string{
		"ai_privacy_status":          "{}",
		"ai_privacy_configure":       `{"enabled":false,"precision":"fp32","revision":""}`,
		"ai_privacy_check_updates":   "{}",
		"ai_privacy_download":        `{"precision":"int8"}`,
		"ai_privacy_cancel_download": "{}",
	}
	for command, args := range commands {
		response := dispatchWithOwner(production.Dispatcher, "", command, args)
		if response.OK || response.Error.Code != ipc.CodeForbidden {
			t.Fatalf("anonymous %s = %+v", command, response)
		}
	}
	var status privacy.Status
	requireStoreTestResponse(t, dispatchWithOwner(production.Dispatcher, "alice", "ai_privacy_status", "{}"), &status)
	if status.Config.Enabled || status.Config.Precision != privacy.INT8 || status.Download.Running || len(status.Installed) != 0 {
		t.Fatalf("initial status = %+v", status)
	}
	requireStoreTestResponse(t, dispatchWithOwner(production.Dispatcher, "alice", "ai_privacy_configure", commands["ai_privacy_configure"]), &status)
	if status.Config.Precision != privacy.FP32 || status.Config.Enabled {
		t.Fatalf("alice status = %+v", status)
	}
	requireStoreTestResponse(t, dispatchWithOwner(production.Dispatcher, "bob", "ai_privacy_status", "{}"), &status)
	if status.Config.Precision != privacy.INT8 || status.Config.Enabled {
		t.Fatalf("bob inherited alice preferences: %+v", status)
	}
	response := dispatchWithOwner(production.Dispatcher, "alice", "ai_privacy_configure", `{"enabled":true,"precision":"int8","revision":""}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam || !strings.Contains(response.Error.Message, "请先下载") {
		t.Fatalf("missing-model remediation hidden: %+v", response)
	}
	requireStoreTestResponse(t, dispatchWithOwner(production.Dispatcher, "alice", "ai_privacy_status", "{}"), &status)
	if status.Config.Enabled || status.Config.Precision != privacy.FP32 {
		t.Fatal("failed activation modified saved selection")
	}
	response = dispatchWithOwner(production.Dispatcher, "alice", "ai_privacy_configure", `{"enabled":false,"precision":"int8","revision":"../outside"}`)
	if response.OK {
		t.Fatal("accepted a path as a revision")
	}
}

func TestProductionAllProfileChatPathsFailClosedForMissingPrivacyModel(t *testing.T) {
	var chatCalls, modelCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/models") {
			modelCalls.Add(1)
			_, _ = w.Write([]byte(`{"data":[{"id":"test"}]}`))
			return
		}
		chatCalls.Add(1)
		_, _ = w.Write([]byte(`{"id":"reply","model":"test","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(upstream.Close)
	production, err := NewProduction(t.Context(), ProductionConfig{DataDir: t.TempDir(), Desktop: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	if err := production.Services.Vault.InitMaster(t.Context(), "test-password"); err != nil {
		t.Fatal(err)
	}
	alice := ipc.WithUserID(t.Context(), "alice")
	bob := ipc.WithUserID(t.Context(), "bob")
	for _, ctx := range []context.Context{alice, bob} {
		_, err := production.Services.Profiles.Save(ctx, profiles.Profile{ID: "profile", Name: "test", BaseURL: upstream.URL + "/v1", APIKey: "test-key", Model: "test", ContextWindow: 1000})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a previously enabled installation whose weights are unavailable.
	// Configure itself refuses this state, so seed it as a missing-on-restart case.
	seed, err := production.Services.Store.SecretProtector().EncryptSecret(alice, strings.Repeat("12", 32))
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := json.Marshal(map[string]any{"enabled": true, "precision": "int8", "revision": strings.Repeat("a", 40), "seed": seed})
	if err := production.Services.Store.SettingSet(alice, "user.ai.privacy.alice", string(saved)); err != nil {
		t.Fatal(err)
	}
	manager := production.Services.Profiles
	paths := []struct {
		name       string
		makeClient func(context.Context) (*provider.Client, error)
	}{
		{"active", func(ctx context.Context) (*provider.Client, error) { return manager.ActiveClient(ctx) }},
		{"empty-id", func(ctx context.Context) (*provider.Client, error) { return manager.ClientFor(ctx, "") }},
		{"explicit-id", func(ctx context.Context) (*provider.Client, error) { return manager.ClientFor(ctx, "profile") }},
	}
	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			client, err := path.makeClient(alice)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ChatBlock(privacy.WithConversation(alice, "conversation"), provider.ChatRequest{Messages: []provider.ChatMessage{provider.UserMessage("do not send this secret")}})
			if err == nil {
				t.Fatal("missing model silently sent an unprotected prompt")
			}
			if chatCalls.Load() != 0 {
				t.Fatal("protected owner contacted upstream")
			}
		})
	}
	for _, args := range []string{`{}`, `{"id":"profile"}`} {
		var result provider.TestResult
		requireStoreTestResponse(t, dispatchWithOwner(production.Dispatcher, "alice", "ai_test_provider", args), &result)
		if result.ChatOK || result.ChatError == nil || !result.ModelsOK {
			t.Fatalf("provider test hid privacy failure: %+v", result)
		}
	}
	if chatCalls.Load() != 0 || modelCalls.Load() != 2 {
		t.Fatalf("upstream calls: chat=%d models=%d", chatCalls.Load(), modelCalls.Load())
	}
	client, err := manager.ActiveClient(bob)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ChatBlock(bob, provider.ChatRequest{Messages: []provider.ChatMessage{provider.UserMessage("normal prompt")}}); err != nil {
		t.Fatal(err)
	}
	if chatCalls.Load() != 1 {
		t.Fatal("owner B default-off behavior changed")
	}
}

func TestPrivacyCommandErrorsExposeOnlySafeRemediation(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		code    ipc.Code
		message string
	}{
		{"configuration", &privacy.UserError{Message: "请先下载并选择本地隐私模型"}, ipc.CodeBadParam, "请先下载"},
		{"platform", fmt.Errorf("load engine: %w", &inference.PlatformError{Message: "本地 AI 脱敏需要 iOS / iPadOS 15.1 或更新版本"}), ipc.CodeUnsupported, "15.1"},
		{"runtime", fmt.Errorf("private runtime path: %w", inference.ErrUnavailable), ipc.CodeUnsupported, "完整 NexTerm"},
		{"filesystem", &os.PathError{Op: "open", Path: "/private/secret-location", Err: os.ErrPermission}, ipc.CodeInternal, "内部错误"},
		{"database", errors.New("database secret-connection-string"), ipc.CodeInternal, "内部错误"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := ipc.NewDispatcher()
			if err := ipc.Register(dispatcher, "privacy_error_test", func(context.Context, *ipc.Call, struct{}) (bool, error) { return privacyCommandResult(false, test.err) }); err != nil {
				t.Fatal(err)
			}
			response := dispatcher.Dispatch(t.Context(), ipc.Request{Command: "privacy_error_test", Args: json.RawMessage("{}")}, ipc.Environment{})
			if response.OK || response.Error == nil || response.Error.Code != test.code || !strings.Contains(response.Error.Message, test.message) {
				t.Fatalf("error response = %+v", response)
			}
			data, _ := json.Marshal(response)
			for _, secret := range []string{"secret-location", "secret-connection-string", "private runtime path"} {
				if strings.Contains(string(data), secret) {
					t.Fatalf("private diagnostic leaked: %s", data)
				}
			}
		})
	}
}
