package production

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestCronScopeForRejectsForeignSessionWithOwnAssetR2(t *testing.T) {
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	services, database, transport, sessionA, _ := composeOutcomeRuntimeContext(t, "echo hi", ctxA, false)
	assetA := scopeSessionAssetID(t, services, ctxA, sessionA)
	assetB, err := database.AssetCreate(ctxB, store.AssetInput{Kind: "ssh", Name: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	sessionB, err := services.Sessions.Connect(ctxB, session.Asset{ID: assetB.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	original := tools.Scope{SessionID: sessionB.ID, TabID: "foreign-tab", AssetID: assetA}
	conversation, err := database.ConvCreate(ctxA, "foreign cron scope", map[string]any{"scope": original})
	if err != nil {
		t.Fatal(err)
	}

	scopeFor := scopeForConversation(database, productionScopeValidator(services))
	_, err = scopeFor(ctxA, cron.Trigger{
		SessionID: conversation.ID, OwnerID: "owner-a", OwnerKind: cron.OwnerKindAccount,
	})
	if !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("foreign session with own asset error = %v, want ErrForbidden", err)
	}
	row, getErr := database.ConvGet(ctxA, conversation.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	persisted, err := scopeFromConversationRow(row)
	if err != nil {
		t.Fatal(err)
	}
	if persisted != original {
		t.Fatalf("rejected cron scope was rewritten: %+v", persisted)
	}
	if calls := transport.calls(); len(calls) != 0 {
		t.Fatalf("rejected cron scope executed commands: %v", calls)
	}
}

func TestAIConversationCreatePersistsResolvedScopeR2(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	services, database, _, oldSessionID, _ := composeOutcomeRuntimeContext(t, "echo hi", ctx, false)
	assetID := scopeSessionAssetID(t, services, ctx, oldSessionID)
	replacementID := replaceDisconnectedScopeSession(t, services, ctx, oldSessionID, assetID)
	dispatcher := ipc.NewDispatcher()
	if err := registerAICommands(dispatcher, services.Profiles, database, productionScopeValidator(services)); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"title": "resolved",
		"scope": tools.Scope{SessionID: oldSessionID, TabID: "retired-tab", AssetID: assetID},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatcher.Dispatch(ctx, ipc.Request{Command: "ai_conversation_create", Args: json.RawMessage(payload)}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("create resolved conversation failed: %+v", response.Error)
	}
	var created conversationDTO
	if err := json.Unmarshal(response.Data, &created); err != nil {
		t.Fatal(err)
	}
	row, err := database.ConvGet(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := scopeFromConversationRow(row)
	if err != nil {
		t.Fatal(err)
	}
	if scope.SessionID != replacementID || scope.TabID != "" || scope.AssetID != assetID {
		t.Fatalf("created scope = %+v, want replacement %q without tab", scope, replacementID)
	}
}

func TestScopeRebindRejectsSoftDeletedAssetR2(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	services, database, _, oldSessionID, _ := composeOutcomeRuntimeContext(t, "echo hi", ctx, false)
	assetID := scopeSessionAssetID(t, services, ctx, oldSessionID)
	original := tools.Scope{SessionID: oldSessionID, TabID: "retired-tab", AssetID: assetID}
	conversation, err := database.ConvCreate(ctx, "deleted asset", map[string]any{"scope": original})
	if err != nil {
		t.Fatal(err)
	}
	replaceDisconnectedScopeSession(t, services, ctx, oldSessionID, assetID)
	if err := database.AssetDelete(ctx, assetID); err != nil {
		t.Fatal(err)
	}

	if _, err := productionScopeValidator(services).ResolveScope(ctx, conversation.ID, original); err == nil {
		t.Fatal("soft-deleted asset was accepted for rebinding despite a live session")
	}
	row, err := database.ConvGet(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := scopeFromConversationRow(row)
	if err != nil {
		t.Fatal(err)
	}
	if persisted != original {
		t.Fatalf("rejected deleted asset changed scope: %+v", persisted)
	}
}

func TestDeviceScopeRebindValidatesOwnerAndRevocationR2(t *testing.T) {
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	services, database, _, _, _ := composeOutcomeRuntimeContext(t, "echo hi", ctxA, false)
	for _, user := range []store.AppUser{
		{ID: "owner-a", Username: "owner-a", Role: "superadmin", PasswordHash: "x", State: "active", CreatedAt: 1, UpdatedAt: 1},
		{ID: "owner-b", Username: "owner-b", Role: "superadmin", PasswordHash: "x", State: "active", CreatedAt: 1, UpdatedAt: 1},
	} {
		if err := database.GORM().Create(&user).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, device := range []store.UserDevice{
		{ID: "device-a", UserID: "owner-a", Name: "A device", Kind: "agent", CreatedAt: 1},
		{ID: "device-b", UserID: "owner-b", Name: "B device", Kind: "agent", CreatedAt: 1},
	} {
		if err := database.GORM().Create(&device).Error; err != nil {
			t.Fatal(err)
		}
	}
	assetID := "device/device-a"
	oldDevice, err := services.Sessions.Connect(ctxA, session.Asset{ID: assetID, Kind: session.KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	original := tools.Scope{SessionID: oldDevice.ID, TabID: "retired-tab", AssetID: assetID}
	conversation, err := database.ConvCreate(ctxA, "device rebind", map[string]any{"scope": original})
	if err != nil {
		t.Fatal(err)
	}
	if err := services.Sessions.Disconnect(oldDevice.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := services.Sessions.Connect(ctxA, session.Asset{ID: assetID, Kind: session.KindDevice})
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := productionScopeValidator(services).ResolveScope(ctxA, conversation.ID, original)
	if err != nil {
		t.Fatalf("owned device did not rebind: %v", err)
	}
	if resolved.SessionID != replacement.ID || resolved.TabID != "" || resolved.AssetID != assetID {
		t.Fatalf("device scope = %+v, want replacement %q without tab", resolved, replacement.ID)
	}
	row, err := database.ConvGet(ctxA, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := scopeFromConversationRow(row)
	if err != nil {
		t.Fatal(err)
	}
	if persisted != resolved {
		t.Fatalf("persisted device scope = %+v, want %+v", persisted, resolved)
	}
	if _, err := productionScopeValidator(services).ResolveScope(ctxA, conversation.ID, tools.Scope{
		SessionID: "missing-device", AssetID: "device/device-b",
	}); err == nil || ipc.NormalizeError(err).Code != ipc.CodeForbidden {
		t.Fatalf("foreign device error = %v, want forbidden", err)
	}
	revokedAt := time.Now().UnixMilli()
	if err := database.GORM().Model(&store.UserDevice{}).
		Where("id = ?", "device-a").Update("revoked_at", revokedAt).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := productionScopeValidator(services).ResolveScope(ctxA, conversation.ID, original); err == nil ||
		ipc.NormalizeError(err).Code != ipc.CodeForbidden {
		t.Fatalf("revoked device error = %v, want forbidden", err)
	}
}
