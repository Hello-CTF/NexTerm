package production

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestAIConversationCreateValidatesScopeBeforePersisting(t *testing.T) {
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	services, database, _, sessionA, _ := composeOutcomeRuntimeContext(t, "echo hi", ctxA, false)
	assetA := scopeSessionAssetID(t, services, ctxA, sessionA)
	assetB, err := database.AssetCreate(ctxB, store.AssetInput{Kind: "ssh", Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	sessionB, err := services.Sessions.Connect(ctxB, session.Asset{ID: assetB.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := ipc.NewDispatcher()
	if err := registerAICommands(dispatcher, services.Profiles, database, productionScopeValidator(services)); err != nil {
		t.Fatal(err)
	}
	create := func(ctx context.Context, title, sessionID, assetID string) ipc.Response {
		args := `{"title":"` + title + `","scope":{"sessionId":"` + sessionID + `"`
		if assetID != "" {
			args += `,"assetId":"` + assetID + `"`
		}
		args += `}}`
		return dispatcher.Dispatch(ctx, ipc.Request{Command: "ai_conversation_create", Args: json.RawMessage(args)}, ipc.Environment{})
	}
	for _, test := range []struct {
		ctx       context.Context
		sessionID string
		ownerID   string
	}{{ctxA, sessionA, "owner-a"}, {ctxB, sessionB.ID, "owner-b"}} {
		response := create(test.ctx, "own", test.sessionID, "")
		if !response.OK {
			t.Fatalf("create own conversation failed: %+v", response.Error)
		}
		var created conversationDTO
		if err := json.Unmarshal(response.Data, &created); err != nil {
			t.Fatal(err)
		}
		row, err := database.ConvGet(test.ctx, created.ID)
		if err != nil || row.OwnerID != test.ownerID {
			t.Fatalf("created conversation owner = %q, %v", row.OwnerID, err)
		}
	}
	for _, test := range []struct {
		name      string
		ctx       context.Context
		sessionID string
		assetID   string
	}{
		{name: "A uses B session", ctx: ctxA, sessionID: sessionB.ID},
		{name: "A uses B session with own asset and healthy candidate", ctx: ctxA, sessionID: sessionB.ID, assetID: assetA},
		{name: "B uses A session", ctx: ctxB, sessionID: sessionA},
		{name: "mismatched asset", ctx: ctxA, sessionID: sessionA, assetID: assetB.ID},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := create(test.ctx, "forbidden", test.sessionID, test.assetID)
			if response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
				t.Fatalf("create = %+v, want forbidden", response)
			}
		})
	}
	for _, test := range []struct {
		ctx     context.Context
		ownerID string
	}{{ctxA, "owner-a"}, {ctxB, "owner-b"}} {
		rows, err := database.ConvList(test.ctx)
		if err != nil || len(rows) != 1 || rows[0].OwnerID != test.ownerID {
			t.Fatalf("conversations after rejected creates = %+v, %v", rows, err)
		}
	}
}
