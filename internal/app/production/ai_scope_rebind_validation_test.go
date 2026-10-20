package production

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestNewMissingSessionCannotUseAssetRebindR3(t *testing.T) {
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	services, database, transport, sessionA, _ := composeOutcomeRuntimeContext(t, "echo hi", ctxA, false)
	assetA := scopeSessionAssetID(t, services, ctxA, sessionA)
	assetB, err := database.AssetCreate(ctxB, store.AssetInput{Kind: "local", Name: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	sessionB, err := services.Sessions.Connect(ctxB, session.Asset{ID: assetB.ID, Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	if err := services.Sessions.Disconnect(sessionB.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := services.Sessions.SessionFor(ctxB, sessionB.ID); err == nil {
		t.Fatal("test setup retained the reaped foreign session")
	}
	dispatcher := ipc.NewDispatcher()
	if err := registerAICommands(dispatcher, services.Profiles, database, productionScopeValidator(services)); err != nil {
		t.Fatal(err)
	}

	for _, missingID := range []string{sessionB.ID, "never-owned-missing-session"} {
		if _, err := services.Agent.Start(ctxA, agent.ChatArgs{
			Message: "must not start",
			Scope:   tools.Scope{SessionID: missingID, AssetID: assetA},
		}, agent.StaticStream(&agent.SliceStream{})); err == nil {
			t.Fatalf("new missing session %q was accepted with a healthy owned candidate", missingID)
		}
		payload, err := json.Marshal(map[string]any{
			"title": "must reject",
			"scope": tools.Scope{SessionID: missingID, AssetID: assetA},
		})
		if err != nil {
			t.Fatal(err)
		}
		response := dispatcher.Dispatch(ctxA, ipc.Request{Command: "ai_conversation_create", Args: json.RawMessage(payload)}, ipc.Environment{})
		if response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
			t.Fatalf("IPC missing session %q = %+v, want forbidden", missingID, response)
		}
	}
	rows, err := database.ConvList(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("missing sessions created conversations: %+v", rows)
	}
	if calls := transport.calls(); len(calls) != 0 {
		t.Fatalf("missing sessions executed commands: %v", calls)
	}
}

func TestPersistedMissingSessionCanRebindR3(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	services, database, _, _, _ := composeOutcomeRuntimeContext(t, "echo hi", ctx, false)
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "local", Name: "owned local"})
	if err != nil {
		t.Fatal(err)
	}
	oldSession, err := services.Sessions.Connect(ctx, session.Asset{ID: asset.ID, Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	original := tools.Scope{SessionID: oldSession.ID, TabID: "retired-tab", AssetID: asset.ID}
	conversation, err := database.ConvCreate(ctx, "persisted missing", map[string]any{"scope": original})
	if err != nil {
		t.Fatal(err)
	}
	if err := services.Sessions.Disconnect(oldSession.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := services.Sessions.SessionFor(ctx, oldSession.ID); err == nil {
		t.Fatal("test setup retained the reaped owned session")
	}
	replacement, err := services.Sessions.Connect(ctx, session.Asset{ID: asset.ID, Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	stream := &agent.SliceStream{}
	if _, err := services.Agent.Start(ctx, agent.ChatArgs{
		ConversationID: conversation.ID, Message: "check the host",
	}, agent.StaticStream(stream)); err != nil {
		t.Fatal(err)
	}
	waitOutcomeClosed(t, stream)
	row, err := database.ConvGet(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := scopeFromConversationRow(row)
	if err != nil {
		t.Fatal(err)
	}
	if scope.SessionID != replacement.ID || scope.TabID != "" || scope.AssetID != asset.ID {
		t.Fatalf("persisted missing scope = %+v, want replacement %q without tab", scope, replacement.ID)
	}
}

func TestConnectedScopeRevalidatesOrdinaryAssetR3(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(context.Context, *store.Store, string) error
	}{
		{
			name: "soft deleted",
			mutate: func(ctx context.Context, database *store.Store, assetID string) error {
				return database.AssetDelete(ctx, assetID)
			},
		},
		{
			name: "owner changed",
			mutate: func(ctx context.Context, database *store.Store, assetID string) error {
				return database.GORM().WithContext(ctx).Model(&store.AssetRow{}).
					Where("id = ?", assetID).Update("owner_id", "owner-b").Error
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := ipc.WithUserID(context.Background(), "owner-a")
			services, database, _, sessionID, _ := composeOutcomeRuntimeContext(t, "echo hi", ctx, false)
			assetID := scopeSessionAssetID(t, services, ctx, sessionID)
			resolver := productionScopeValidator(services)
			if err := resolver.ValidateScope(ctx, tools.Scope{SessionID: sessionID, AssetID: assetID}); err != nil {
				t.Fatalf("valid connected scope rejected during setup: %v", err)
			}
			if err := test.mutate(ctx, database, assetID); err != nil {
				t.Fatal(err)
			}
			if err := resolver.ValidateScope(ctx, tools.Scope{SessionID: sessionID, AssetID: assetID}); err == nil {
				t.Fatal("connected scope bypassed current asset validation")
			}
			if err := resolver.ValidateScope(ctx, tools.Scope{AssetID: assetID}); err == nil {
				t.Fatal("asset-only scope bypassed current asset validation")
			}
		})
	}
}

func TestConnectedScopeRevalidatesDeviceRevocationR3(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	services, database, _, _, _ := composeOutcomeRuntimeContext(t, "echo hi", ctx, false)
	user := store.AppUser{ID: "owner-a", Username: "owner-a", Role: "superadmin", PasswordHash: "x", State: "active", CreatedAt: 1, UpdatedAt: 1}
	if err := database.GORM().Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	device := store.UserDevice{ID: "device-a", UserID: "owner-a", Name: "A device", Kind: "agent", CreatedAt: 1}
	if err := database.GORM().Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	assetID := "device/device-a"
	connected, err := services.Sessions.Connect(ctx, session.Asset{ID: assetID, Kind: session.KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	resolver := productionScopeValidator(services)
	if err := resolver.ValidateScope(ctx, tools.Scope{SessionID: connected.ID, AssetID: assetID}); err != nil {
		t.Fatalf("valid connected device rejected during setup: %v", err)
	}
	if err := database.GORM().Model(&store.UserDevice{}).
		Where("id = ?", device.ID).Update("revoked_at", time.Now().UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if err := resolver.ValidateScope(ctx, tools.Scope{SessionID: connected.ID, AssetID: assetID}); err == nil {
		t.Fatal("connected revoked device was accepted")
	}
	if err := resolver.ValidateScope(ctx, tools.Scope{AssetID: assetID}); err == nil {
		t.Fatal("asset-only revoked device was accepted")
	}
}

func TestAIConversationCreatePreservesNullScopeR3(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	services, database, _, _, _ := composeOutcomeRuntimeContext(t, "echo hi", ctx, false)
	dispatcher := ipc.NewDispatcher()
	if err := registerAICommands(dispatcher, services.Profiles, database, productionScopeValidator(services)); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`{"title":"explicit-null","scope":null}`, `{"title":"omitted-null"}`} {
		response := dispatcher.Dispatch(ctx, ipc.Request{Command: "ai_conversation_create", Args: json.RawMessage(payload)}, ipc.Environment{})
		if !response.OK {
			t.Fatalf("plain create failed: %+v", response.Error)
		}
		var created conversationDTO
		if err := json.Unmarshal(response.Data, &created); err != nil {
			t.Fatal(err)
		}
		var wrapped map[string]json.RawMessage
		if err := json.Unmarshal(created.Scope, &wrapped); err != nil {
			t.Fatal(err)
		}
		if len(wrapped) != 1 || string(wrapped["scope"]) != "null" {
			t.Fatalf("plain create scope = %s, want {\"scope\":null}", created.Scope)
		}
		row, err := database.ConvGet(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.ScopeJSON != `{"scope":null}` {
			t.Fatalf("persisted plain scope = %s", row.ScopeJSON)
		}
	}
}
