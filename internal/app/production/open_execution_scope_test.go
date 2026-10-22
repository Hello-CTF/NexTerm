package production

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestOpenRecoveredAccountDropsOpenForTargetValidation(t *testing.T) {
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	services, database, _, _, _ := composeOutcomeRuntimeContext(t, "echo hi", ctxA, false)
	assetB, err := database.AssetCreate(ctxB, store.AssetInput{Kind: "ssh", Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	sessionB, err := services.Sessions.Connect(ctxB, session.Asset{ID: assetB.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	executionCtx := owner.AccountContext(owner.WithOpenAccess(context.Background()), "owner-a")
	if owner.OpenAccess(executionCtx) {
		t.Fatal("recovered account retained Open access")
	}
	if err := productionGrantTargetValidator(services)(executionCtx, assetB.ID); err == nil {
		t.Fatal("foreign target was allowed after Open account recovery")
	}
	if err := productionScopeValidator(services).ValidateScope(executionCtx, tools.Scope{SessionID: sessionB.ID}); err == nil {
		t.Fatal("foreign session was allowed after Open account recovery")
	}
	sharedCtx := owner.AccountContext(owner.WithOpenAccess(context.Background()), "")
	if !owner.OpenAccess(sharedCtx) {
		t.Fatal("ownerless Open execution lost shared capability")
	}
}
