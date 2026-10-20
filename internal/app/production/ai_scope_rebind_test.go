package production

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func scopeSessionAssetID(t *testing.T, services *ProductionServices, ctx context.Context, sessionID string) string {
	t.Helper()
	connected, err := services.Sessions.SessionFor(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return connected.Asset().ID
}

func replaceDisconnectedScopeSession(t *testing.T, services *ProductionServices, ctx context.Context, oldSessionID, assetID string) string {
	t.Helper()
	if err := services.Sessions.Disconnect(oldSessionID); err != nil {
		t.Fatal(err)
	}
	replacement, err := services.Sessions.Connect(ctx, session.Asset{ID: assetID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == oldSessionID {
		t.Fatal("test setup reused the disconnected session")
	}
	return replacement.ID
}

func TestRunStartRebindsDeadSessionAndPersistsScope(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	services, database, _, oldSessionID, _ := composeOutcomeRuntimeContext(t, "echo hi", ctx, false)
	assetID := scopeSessionAssetID(t, services, ctx, oldSessionID)
	conversation, err := database.ConvCreate(ctx, "rebind", map[string]any{"scope": tools.Scope{
		SessionID: oldSessionID, TabID: "retired-tab", AssetID: assetID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	replacementID := replaceDisconnectedScopeSession(t, services, ctx, oldSessionID, assetID)

	stream := &agent.SliceStream{}
	if _, err := services.Agent.Start(ctx, agent.ChatArgs{
		ConversationID: conversation.ID,
		Message:        "check the host",
		Scope:          tools.Scope{SessionID: "client-replacement-must-be-ignored"},
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
	if scope.SessionID != replacementID || scope.TabID != "" || scope.AssetID != assetID {
		t.Fatalf("persisted scope = %+v, want replacement %q without tab", scope, replacementID)
	}
}

func TestCronScopeForRebindsAndPersistsWithoutChangingOwner(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	services, database, _, oldSessionID, _ := composeOutcomeRuntimeContext(t, "echo hi", ctx, false)
	assetID := scopeSessionAssetID(t, services, ctx, oldSessionID)
	conversation, err := database.ConvCreate(ctx, "cron rebind", map[string]any{"scope": tools.Scope{
		SessionID: oldSessionID, TabID: "retired-tab", AssetID: assetID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	replacementID := replaceDisconnectedScopeSession(t, services, ctx, oldSessionID, assetID)
	scopeFor := scopeForConversation(database, productionScopeValidator(services))

	scope, err := scopeFor(ctx, cron.Trigger{
		SessionID: conversation.ID, OwnerID: "owner-a", OwnerKind: cron.OwnerKindAccount,
	})
	if err != nil {
		t.Fatal(err)
	}
	if scope.SessionID != replacementID || scope.TabID != "" || scope.AssetID != assetID {
		t.Fatalf("cron scope = %+v, want replacement %q without tab", scope, replacementID)
	}
	row, err := database.ConvGet(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := scopeFromConversationRow(row)
	if err != nil {
		t.Fatal(err)
	}
	if persisted != scope || row.OwnerID != "owner-a" {
		t.Fatalf("persisted scope/owner = %+v/%q, want %+v/owner-a", persisted, row.OwnerID, scope)
	}
	if _, err := scopeFor(ctx, cron.Trigger{
		SessionID: conversation.ID, OwnerID: "owner-b", OwnerKind: cron.OwnerKindAccount,
	}); !errors.Is(err, cron.ErrForeignScope) {
		t.Fatalf("foreign cron owner error = %v, want ErrForeignScope", err)
	}
}

func TestRunStartRejectsForeignSessionAndAssetWithoutCreatingConversation(t *testing.T) {
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

	for _, test := range []struct {
		name  string
		scope tools.Scope
	}{
		{name: "foreign session and asset", scope: tools.Scope{SessionID: sessionB.ID, AssetID: assetB.ID}},
		{name: "foreign session with own asset and healthy candidate", scope: tools.Scope{SessionID: sessionB.ID, AssetID: assetA}},
		{name: "forged foreign asset on own session", scope: tools.Scope{SessionID: sessionA, AssetID: assetB.ID}},
		{name: "foreign session without asset keeps legacy rejection", scope: tools.Scope{SessionID: sessionB.ID}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := services.Agent.Start(ctxA, agent.ChatArgs{
				Message: "must not start", Scope: test.scope,
			}, agent.StaticStream(&agent.SliceStream{})); err == nil {
				t.Fatal("foreign or forged scope was accepted")
			}
		})
	}
	rows, err := database.ConvList(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rejected client scopes created conversations: %+v", rows)
	}
	if calls := transport.calls(); len(calls) != 0 {
		t.Fatalf("rejected scopes executed commands: %v", calls)
	}
	if assetA == assetB.ID {
		t.Fatal("test setup reused asset IDs")
	}
}

func TestCronWithoutLiveSessionRecordsClearLastError(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	services, database, _, oldSessionID, _ := composeOutcomeRuntimeContext(t, "echo hi", ctx, false)
	assetID := scopeSessionAssetID(t, services, ctx, oldSessionID)
	conversation, err := database.ConvCreate(ctx, "cron offline", map[string]any{"scope": tools.Scope{
		SessionID: oldSessionID, TabID: "retired-tab", AssetID: assetID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := services.cron.scheduler.Register(ctx, cron.Registration{
		SessionID: conversation.ID, Prompt: "check disk", Schedule: "* * * * *",
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.OwnerID != "owner-a" || job.OwnerKind != cron.OwnerKindAccount {
		t.Fatalf("cron owner = %q/%q", job.OwnerID, job.OwnerKind)
	}
	if err := services.Sessions.Disconnect(oldSessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().ExecContext(ctx,
		`UPDATE cron_job SET next_run_at = ? WHERE id = ?`,
		time.Now().Add(-time.Minute).UnixMilli(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := services.cron.Start(ctx); err != nil {
		t.Fatal(err)
	}

	var finished cron.Job
	deadline := time.Now().Add(30 * time.Second)
	for {
		current, err := services.cron.scheduler.Get(ctx, conversation.ID, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !current.LastRunAt.IsZero() && !current.Running() {
			finished = current
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for cron job %s: %+v", job.ID, current)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !strings.Contains(finished.LastError, "没有当前用户可用的健康在线会话") ||
		!strings.Contains(finished.LastError, "手动连接") {
		t.Fatalf("cron lastError = %q", finished.LastError)
	}
	if finished.ConsecutiveFailures != 1 {
		t.Fatalf("cron failures = %d, want 1", finished.ConsecutiveFailures)
	}
	row, err := database.ConvGet(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := scopeFromConversationRow(row)
	if err != nil {
		t.Fatal(err)
	}
	if scope.SessionID != oldSessionID || scope.TabID != "retired-tab" {
		t.Fatalf("failed rebind changed scope: %+v", scope)
	}
	infos, err := services.Sessions.ListSessionsFor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != oldSessionID || infos[0].Status != session.StatusDisconnected {
		t.Fatalf("cron connected or replaced sessions automatically: %+v", infos)
	}
}

func TestLegacyConversationWithoutAssetKeepsDeadSessionScope(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	services, database, _, oldSessionID, _ := composeOutcomeRuntimeContext(t, "echo hi", ctx, false)
	legacy := tools.Scope{SessionID: oldSessionID}
	conversation, err := database.ConvCreate(ctx, "legacy", map[string]any{"scope": legacy})
	if err != nil {
		t.Fatal(err)
	}
	before, err := database.ConvGet(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := services.Sessions.Disconnect(oldSessionID); err != nil {
		t.Fatal(err)
	}

	resolved, err := productionScopeValidator(services).ResolveScope(ctx, conversation.ID, legacy)
	if err != nil {
		t.Fatalf("legacy dead session behavior changed: %v", err)
	}
	if resolved != legacy {
		t.Fatalf("legacy scope = %+v, want %+v", resolved, legacy)
	}
	after, err := database.ConvGet(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ScopeJSON != before.ScopeJSON {
		t.Fatalf("legacy scope was rewritten: before %s after %s", before.ScopeJSON, after.ScopeJSON)
	}
}
