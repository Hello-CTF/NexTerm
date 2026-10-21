package production

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/jackc/pgx/v5"
)

// aiGrantDBScript is a scripted chat provider: the first completion request
// emits one db_query tool call, and every request that already carries a tool
// result finishes the run with a plain "done".
type aiGrantDBScript struct {
	mu       sync.Mutex
	requests int
	sql      string
}

func (s *aiGrantDBScript) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	var parsed spawnScriptRequest
	if err := json.Unmarshal(body, &parsed); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.requests++
	s.mu.Unlock()

	writer.Header().Set("Content-Type", "text/event-stream")
	if !spawnHasRole(&parsed, "tool") {
		encoded, err := json.Marshal(tools.DBQueryArgs{SQL: s.sql})
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
		spawnWriteToolCall(writer, "call-db-1", "db_query", string(encoded))
		return
	}
	spawnWriteContent(writer, "done")
}

// The AI grant identity chain against a real PostgreSQL server: a chat scoped
// to a live database connection evaluates db_query against the connection's
// real DB asset. Without a grant the write asks for confirmation; a permanent
// db_query grant created through the typed IPC authorizes it; revoke restores
// confirmation. Cross-owner connections and stale (disconnected) handles are
// rejected at scope validation instead of inheriting grant authority.
func TestProductionAIGrantDBIdentityIntegration(t *testing.T) {
	if os.Getenv(dbTunnelIntegrationFlag) != "1" {
		t.Skipf("set %s=1 and NEXTERM_TEST_DB_DSN to run the real PostgreSQL AI grant test", dbTunnelIntegrationFlag)
	}
	if backend := os.Getenv("NEXTERM_TEST_DB_BACKEND"); backend != "" && backend != "postgres" {
		t.Skipf("NEXTERM_TEST_DB_BACKEND=%s selects another database integration test", backend)
	}
	dsn := os.Getenv("NEXTERM_TEST_DB_DSN")
	if dsn == "" {
		t.Fatal("NEXTERM_TEST_DB_DSN is required for the real PostgreSQL AI grant test")
	}
	existing, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse NEXTERM_TEST_DB_DSN: %v", err)
	}

	ctx := context.Background()
	adminA := ipc.WithRole(ipc.WithUserID(ctx, "owner-a"), "superadmin")
	adminB := ipc.WithRole(ipc.WithUserID(ctx, "owner-b"), "superadmin")

	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	initTestVault(t, ctx, database)
	profileManager, err := profiles.NewManager(ctx, database, profiles.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	script := &aiGrantDBScript{sql: "CREATE TABLE IF NOT EXISTS ai_grant_probe (id integer PRIMARY KEY, note text)"}
	providerServer := httptest.NewServer(script)
	t.Cleanup(providerServer.Close)
	if _, err := profileManager.Save(adminA, profiles.Profile{
		BaseURL: providerServer.URL, APIKey: "test-key", Model: "test-model",
		ContextWindow: 32768, Stream: true,
	}); err != nil {
		t.Fatal(err)
	}

	sessions := session.NewManager(session.Config{})
	t.Cleanup(func() { _ = sessions.Close() })
	service := newProductionDatabaseService(database, dbTunnelIntegrationPassword(existing.Password), sessions)
	t.Cleanup(func() { _ = service.Close() })

	credentialID, err := database.CredentialPut(adminA, store.CredentialInput{Name: "PostgreSQL", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}})
	if err != nil {
		t.Fatal(err)
	}
	optionsJSON, err := json.Marshal(map[string]any{"database": existing.Database})
	if err != nil {
		t.Fatal(err)
	}
	host, username, port := existing.Host, existing.User, int32(existing.Port)
	asset, err := database.AssetCreate(adminA, store.AssetInput{
		Kind: "postgres", Name: "AI grant target", Host: &host, Port: &port, Username: &username,
		CredID: &credentialID, OptionsJSON: string(optionsJSON),
	})
	if err != nil {
		t.Fatal(err)
	}

	services := &ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions, Database: service}
	if err := composeAIRuntime(adminA, services); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(services.closeAIRuntime)

	connected, err := service.Connect(adminA, db.ConnectArgs{AssetID: asset.ID})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	probeConn := func() *pgx.Conn {
		t.Helper()
		// A fresh context: t.Context() is already canceled when cleanups run.
		probeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		direct, err := pgx.ConnectConfig(probeCtx, existing.Copy())
		if err != nil {
			t.Fatalf("verification connection: %v", err)
		}
		return direct
	}
	probeExists := func() bool {
		direct := probeConn()
		defer func() { _ = direct.Close(context.Background()) }()
		var count int
		if err := direct.QueryRow(context.Background(), "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'ai_grant_probe'").Scan(&count); err != nil {
			t.Fatalf("probe query: %v", err)
		}
		return count == 1
	}
	t.Cleanup(func() {
		direct := probeConn()
		defer func() { _ = direct.Close(context.Background()) }()
		if _, err := direct.Exec(context.Background(), "DROP TABLE IF EXISTS ai_grant_probe"); err != nil {
			t.Fatalf("drop probe table: %v", err)
		}
	})
	if probeExists() {
		t.Fatal("probe table exists before the test exercised any grant flow")
	}

	dispatcher := ipc.NewDispatcher()
	if err := agent.Module(services.Agent).RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	dispatch := func(ctx context.Context, command, args string) ipc.Response {
		t.Helper()
		return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
	}
	startChat := func(ctx context.Context) (*agent.SliceStream, agent.StartResponse, error) {
		t.Helper()
		stream := &agent.SliceStream{}
		response, err := services.Agent.Start(ctx, agent.ChatArgs{Message: "create the probe table", Scope: tools.Scope{ConnID: connected.ConnID}}, agent.StaticStream(stream))
		return stream, response, err
	}

	// No grant: the write ruling needs confirmation, and denying it leaves the
	// database untouched.
	stream, response, err := startChat(adminA)
	if err != nil {
		t.Fatalf("start ungranted chat: %v", err)
	}
	confirmation := waitOutcomeEvent(t, stream, "confirmRequired")
	if err := services.Agent.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "deny"}); err != nil {
		t.Fatal(err)
	}
	waitOutcomeClosed(t, stream)
	if probeExists() {
		t.Fatal("denied confirmation executed the database write")
	}

	// A permanent db_query grant created through the typed IPC authorizes the
	// same write without another confirmation.
	if granted := dispatch(adminA, "ai_grant_set", `{"deviceId":"`+asset.ID+`","kinds":["db_query"]}`); !granted.OK {
		t.Fatalf("ai_grant_set = %+v", granted.Error)
	}
	stream, _, err = startChat(adminA)
	if err != nil {
		t.Fatalf("start granted chat: %v", err)
	}
	grantedEvents := waitOutcomeClosed(t, stream)
	executed := false
	for _, event := range grantedEvents {
		if event.Type == "confirmRequired" {
			t.Fatal("granted chat asked for confirmation")
		}
		if event.Type == "toolResult" && event.OK {
			executed = true
		}
	}
	if !executed {
		t.Fatalf("granted chat did not execute the write: %+v", grantedEvents)
	}
	if !probeExists() {
		t.Fatal("granted write left no probe table behind")
	}

	// Revoke through the same typed IPC restores confirmation immediately.
	if revoked := dispatch(adminA, "ai_grant_revoke", `{"deviceId":"`+asset.ID+`"}`); !revoked.OK {
		t.Fatalf("ai_grant_revoke = %+v", revoked.Error)
	}
	stream, response, err = startChat(adminA)
	if err != nil {
		t.Fatalf("start revoked chat: %v", err)
	}
	confirmation = waitOutcomeEvent(t, stream, "confirmRequired")
	if err := services.Agent.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "deny"}); err != nil {
		t.Fatal(err)
	}
	waitOutcomeClosed(t, stream)

	// A foreign owner cannot even start a chat on owner A's connection: scope
	// validation resolves the connection server-side and rejects it.
	if _, _, err = startChat(adminB); err == nil {
		t.Fatal("cross-owner chat started on another owner's connection")
	} else {
		var appErr *ipc.Error
		if !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
			t.Fatalf("cross-owner chat error = %v, want forbidden", err)
		}
	}

	// Disconnecting the handle makes the persisted scope stale: the next chat
	// fails validation instead of silently keeping the grant authority.
	if err := service.Disconnect(adminA, connected.ConnID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = startChat(adminA); err == nil {
		t.Fatal("chat started on a disconnected (stale) connection")
	}
}
