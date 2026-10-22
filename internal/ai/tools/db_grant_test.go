package tools

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// identityDatabase is a Database test double with a mutable, owner-aware
// conn→asset identity map modeling db.Service.AssetIDOf: an entry resolves
// only for its owning account, and dropping it simulates a disconnected or
// revoked (stale) connection.
type identityDatabase struct {
	fakeDatabase
	mu      sync.Mutex
	assets  map[string]string
	owners  map[string]string
	queries int
}

func (d *identityDatabase) AssetIDOf(ctx context.Context, connID string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	assetID, ok := d.assets[connID]
	if !ok {
		return "", false
	}
	if owner := d.owners[connID]; owner != "" {
		userID, hasUser := ipc.UserIDFromContext(ctx)
		if !hasUser || userID != owner {
			return "", false
		}
	}
	return assetID, true
}

func (d *identityDatabase) Query(context.Context, string, string, uint64, time.Duration) (db.QueryResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queries++
	return db.QueryResult{Columns: []string{"n"}, Rows: [][]any{{"1"}}}, nil
}

func (d *identityDatabase) executed() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.queries
}

func (d *identityDatabase) bind(connID, assetID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.assets[connID] = assetID
}

func (d *identityDatabase) drop(connID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.assets, connID)
}

type dbGrantHarness struct {
	execution *Execution
	database  *identityDatabase
	recorder  *chatGrantRecorder
	grants    *guard.Grants
}

func newDBGrantHarness(t *testing.T, grants *guard.Grants, recorder *chatGrantRecorder, scope Scope, sessionAssetID string) *dbGrantHarness {
	t.Helper()
	database := &identityDatabase{assets: map[string]string{}, owners: map[string]string{}}
	terminal := &chatGrantTerminal{assetID: sessionAssetID}
	deps := Dependencies{
		Terminal:   terminal,
		TabSession: func(string) string { return scope.SessionID },
		Database:   database,
		Grants:     grants,
	}
	harness := &dbGrantHarness{database: database, recorder: recorder, grants: grants}
	harness.execution = &Execution{
		JobID:      "job-db-grant",
		Registry:   NewRegistry(deps),
		Scope:      scope,
		Permission: guard.Config{Mode: guard.ReadWrite},
		Memory:     guard.NewMemory(),
	}
	return harness
}

// invoke drives the real chat tool path with an explicit caller context, so
// owner-scoped grants see the same identity the production request carries.
func (h *dbGrantHarness) invoke(ctx context.Context, callID, name, args string) ([]*schema.Message, error) {
	einoTools, err := h.execution.Tools()
	if err != nil {
		return nil, err
	}
	node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{Tools: einoTools, ExecuteSequentially: true})
	if err != nil {
		return nil, err
	}
	message := schema.AssistantMessage("", []schema.ToolCall{{ID: callID, Function: schema.FunctionCall{Name: name, Arguments: args}}})
	return node.Invoke(ctx, message)
}

func requireDBGrantConfirm(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a confirmation interrupt")
	}
	if _, ok := compose.IsInterruptRerunError(err); !ok {
		t.Fatalf("error is not an interrupt: %v", err)
	}
}

func requireDBGrantExecuted(t *testing.T, messages []*schema.Message, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected interrupt: %v", err)
	}
	if len(messages) != 1 || !strings.Contains(messages[0].Content, "1") {
		t.Fatalf("tool result = %+v", messages)
	}
}

const dbWriteCall = `{"sql":"INSERT INTO t VALUES (1)"}`

// The R-14 core negative: a database tool call must be evaluated against the
// connection's real DB asset, never against the session asset the chat is
// scoped to. A db_query grant on the session asset must not authorize a write
// through a connection that belongs to a different, ungranted asset.
func TestDBGrantUsesConnectionIdentityNotSessionAsset(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-session", []guard.GrantKind{guard.GrantKindDBQuery}); err != nil {
		t.Fatal(err)
	}
	h := newDBGrantHarness(t, grants, recorder, Scope{SessionID: "session-1", TabID: "tab-1", ConnID: "conn-1"}, "asset-session")
	h.database.bind("conn-1", "asset-db")

	_, err := h.invoke(context.Background(), "call-1", "db_query", dbWriteCall)
	requireDBGrantConfirm(t, err)
	if h.database.executed() != 0 {
		t.Fatal("session-asset grant authorized a write on the connection's real asset")
	}
	if recorder.find("use", "asset-session") != nil {
		t.Fatal("grant use audited for the session asset on a database call")
	}

	if _, err := grants.Grant(context.Background(), "asset-db", []guard.GrantKind{guard.GrantKindDBQuery}); err != nil {
		t.Fatal(err)
	}
	messages, err := h.invoke(context.Background(), "call-2", "db_query", dbWriteCall)
	requireDBGrantExecuted(t, messages, err)
	if h.database.executed() != 1 {
		t.Fatalf("queries = %d, want 1", h.database.executed())
	}
	if recorder.find("use", "asset-db") == nil {
		t.Fatal("grant use not audited for the connection asset")
	}
}

// Cross-owner negatives: another owner's permanent grant must never cover the
// caller's connection, and a connection owned by another account resolves no
// identity at all instead of inheriting the caller's grants.
func TestDBGrantCrossOwnerRejected(t *testing.T) {
	settings := &chatGrantSettings{values: make(map[string]string)}
	recorder := &chatGrantRecorder{}
	grants, err := guard.NewGrants(context.Background(), settings, recorder.record, guard.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	if _, err := grants.Grant(ctxB, "asset-db", []guard.GrantKind{guard.GrantKindDBQuery}); err != nil {
		t.Fatal(err)
	}
	h := newDBGrantHarness(t, grants, recorder, Scope{ConnID: "conn-1"}, "")
	h.database.bind("conn-1", "asset-db")
	h.database.owners["conn-1"] = "owner-a"

	// Owner A uses its own connection: owner B's grant on the same asset id
	// must not leak across owners.
	_, err = h.invoke(ctxA, "call-1", "db_query", dbWriteCall)
	requireDBGrantConfirm(t, err)
	if h.database.executed() != 0 {
		t.Fatal("another owner's grant authorized this owner's connection")
	}

	// Owner B tries the connection owned by owner A: the identity resolves to
	// nothing, so even B's own grant on that asset cannot authorize the call.
	_, err = h.invoke(ctxB, "call-2", "db_query", dbWriteCall)
	requireDBGrantConfirm(t, err)
	if h.database.executed() != 0 {
		t.Fatal("cross-owner connection inherited the caller's grant")
	}
}

// A stale connection (disconnected or logged out between calls) loses its
// identity immediately; the previously bound grant evaluation must not
// authorize the next call.
func TestDBGrantStaleConnectionLosesIdentity(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-db", []guard.GrantKind{guard.GrantKindDBQuery}); err != nil {
		t.Fatal(err)
	}
	h := newDBGrantHarness(t, grants, recorder, Scope{ConnID: "conn-1"}, "")
	h.database.bind("conn-1", "asset-db")

	messages, err := h.invoke(context.Background(), "call-1", "db_query", dbWriteCall)
	requireDBGrantExecuted(t, messages, err)
	if h.database.executed() != 1 {
		t.Fatalf("queries = %d, want 1", h.database.executed())
	}

	h.database.drop("conn-1")
	_, err = h.invoke(context.Background(), "call-2", "db_query", dbWriteCall)
	requireDBGrantConfirm(t, err)
	if h.database.executed() != 1 {
		t.Fatal("stale connection kept its grant authority")
	}
}

// An inline connection has no database asset: no identity may be invented for
// it, no binding may be recorded, and existing asset grants must not apply.
func TestDBGrantInlineConnectionGetsNoInventedIdentity(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-db", []guard.GrantKind{guard.GrantKindDBQuery}); err != nil {
		t.Fatal(err)
	}
	h := newDBGrantHarness(t, grants, recorder, Scope{ConnID: "conn-inline"}, "")

	_, err := h.invoke(context.Background(), "call-1", "db_query", dbWriteCall)
	requireDBGrantConfirm(t, err)
	if h.database.executed() != 0 {
		t.Fatal("inline connection executed under an asset grant")
	}
	if _, resolveErr := grants.ResolveConnAsset(context.Background(), "conn-inline"); resolveErr == nil {
		t.Fatal("inline connection recorded an invented asset binding")
	}
}

// Rebinding the same connId to a different asset must not inherit the previous
// asset's grant; granting the new asset restores authorization.
func TestDBGrantRebindDoesNotInheritPreviousAsset(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-db", []guard.GrantKind{guard.GrantKindDBQuery}); err != nil {
		t.Fatal(err)
	}
	h := newDBGrantHarness(t, grants, recorder, Scope{ConnID: "conn-1"}, "")
	h.database.bind("conn-1", "asset-db")

	messages, err := h.invoke(context.Background(), "call-1", "db_query", dbWriteCall)
	requireDBGrantExecuted(t, messages, err)

	h.database.bind("conn-1", "asset-other")
	_, err = h.invoke(context.Background(), "call-2", "db_query", dbWriteCall)
	requireDBGrantConfirm(t, err)
	if h.database.executed() != 1 {
		t.Fatal("rebound connection inherited the previous asset's grant")
	}

	if _, err := grants.Grant(context.Background(), "asset-other", []guard.GrantKind{guard.GrantKindDBQuery}); err != nil {
		t.Fatal(err)
	}
	messages, err = h.invoke(context.Background(), "call-3", "db_query", dbWriteCall)
	requireDBGrantExecuted(t, messages, err)
	if h.database.executed() != 2 {
		t.Fatalf("queries = %d, want 2", h.database.executed())
	}
}

func TestDBGrantRevokeRestoresConfirmImmediately(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-db", []guard.GrantKind{guard.GrantKindDBQuery}); err != nil {
		t.Fatal(err)
	}
	h := newDBGrantHarness(t, grants, recorder, Scope{ConnID: "conn-1"}, "")
	h.database.bind("conn-1", "asset-db")

	messages, err := h.invoke(context.Background(), "call-1", "db_query", dbWriteCall)
	requireDBGrantExecuted(t, messages, err)
	if err := grants.Revoke(context.Background(), "asset-db"); err != nil {
		t.Fatal(err)
	}
	_, err = h.invoke(context.Background(), "call-2", "db_query", dbWriteCall)
	requireDBGrantConfirm(t, err)
	if h.database.executed() != 1 {
		t.Fatal("query executed after the grant was revoked")
	}
}

// Production behavior (M74) must survive the identity fix: session memory
// never auto-authorizes a production DB asset, while an explicit permanent
// grant still does.
func TestDBGrantProductionKeepsExplicitConfirmation(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants, err := guard.NewGrants(context.Background(), &chatGrantSettings{values: make(map[string]string)}, recorder.record,
		guard.WithEnvironmentResolver(func(_ context.Context, assetID string) (string, error) {
			if assetID == "asset-db" {
				return "production", nil
			}
			return "", nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	h := newDBGrantHarness(t, grants, recorder, Scope{ConnID: "conn-1"}, "")
	h.database.bind("conn-1", "asset-db")
	h.execution.Memory.Add(guard.KindDBWrite)

	_, err = h.invoke(context.Background(), "call-1", "db_query", dbWriteCall)
	requireDBGrantConfirm(t, err)
	if h.database.executed() != 0 {
		t.Fatal("session memory authorized a production database write")
	}

	if _, err := grants.Grant(context.Background(), "asset-db", []guard.GrantKind{guard.GrantKindDBQuery}); err != nil {
		t.Fatal(err)
	}
	messages, err := h.invoke(context.Background(), "call-2", "db_query", dbWriteCall)
	requireDBGrantExecuted(t, messages, err)
	if h.database.executed() != 1 {
		t.Fatal("explicit permanent grant did not authorize the production write")
	}
}

// A Database backend without the identity seam disables the grant branch
// instead of falling back to the session asset: the plain permission mode
// still applies, so nothing is denied that the mode allows.
func TestDBGrantWithoutIdentitySeamDisablesGrantBranch(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-session", []guard.GrantKind{guard.GrantKindDBQuery}); err != nil {
		t.Fatal(err)
	}
	database := &fakeDatabase{}
	deps := Dependencies{
		Terminal:   &chatGrantTerminal{assetID: "asset-session"},
		TabSession: func(string) string { return "session-1" },
		Database:   database,
		Grants:     grants,
	}
	execution := &Execution{
		JobID:      "job-db-grant",
		Registry:   NewRegistry(deps),
		Scope:      Scope{SessionID: "session-1", TabID: "tab-1", ConnID: "conn-1"},
		Permission: guard.Config{Mode: guard.ReadWrite},
		Memory:     guard.NewMemory(),
	}
	h := &dbGrantHarness{execution: execution, recorder: recorder, grants: grants}
	_, err := h.invoke(context.Background(), "call-1", "db_query", dbWriteCall)
	requireDBGrantConfirm(t, err)
}

// A granted connection keeps the device-grant precedence over the global
// read-only mode, matching the semantics the grant panel advertises.
func TestDBGrantOverridesReadOnlyMode(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-db", []guard.GrantKind{guard.GrantKindDBQuery}); err != nil {
		t.Fatal(err)
	}
	h := newDBGrantHarness(t, grants, recorder, Scope{ConnID: "conn-1"}, "")
	h.database.bind("conn-1", "asset-db")
	h.execution.Permission = guard.Config{Mode: guard.ReadOnly}

	messages, err := h.invoke(context.Background(), "call-1", "db_query", dbWriteCall)
	requireDBGrantExecuted(t, messages, err)
	if h.database.executed() != 1 {
		t.Fatal("device grant did not take precedence over the global read-only mode")
	}
}
