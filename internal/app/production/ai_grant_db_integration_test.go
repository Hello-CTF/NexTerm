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
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/jackc/pgx/v5"
)

// aiGrantDBScript is a scripted chat provider: the first completion request
// emits one tool call (db_query by default, exec_commands in exec mode), and
// every request that already carries a tool result finishes the run with a
// plain "done".
type aiGrantDBScript struct {
	mu       sync.Mutex
	requests int
	sql      string
	command  string
	execMode bool
}

func (s *aiGrantDBScript) setExecMode(execMode bool) {
	s.mu.Lock()
	s.execMode = execMode
	s.mu.Unlock()
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
	execMode := s.execMode
	s.mu.Unlock()

	writer.Header().Set("Content-Type", "text/event-stream")
	if !spawnHasRole(&parsed, "tool") {
		if execMode {
			encoded, err := json.Marshal(tools.ExecCommandsArgs{Commands: []string{s.command}})
			if err != nil {
				http.Error(writer, err.Error(), http.StatusInternalServerError)
				return
			}
			spawnWriteToolCall(writer, "call-exec-1", "exec_commands", string(encoded))
			return
		}
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

// The AI grant identity chain against a real PostgreSQL server. A chat scoped
// to a live database connection evaluates db_query against the connection's
// real DB asset: without a grant the write asks for confirmation, a permanent
// db_query grant created through the typed IPC authorizes it, and revoke
// restores confirmation. A mixed terminal+database scope binds each
// confirmation to the actual tool's asset — the database call carries the
// production DB asset and its environment challenge, the terminal call the
// session asset — and a connection that disappears while its confirmation is
// pending rejects the answer. Cross-owner connections and stale (disconnected)
// handles are rejected at scope validation instead of inheriting grant
// authority.
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
	script := &aiGrantDBScript{sql: "CREATE TABLE IF NOT EXISTS ai_grant_probe (id integer PRIMARY KEY, note text)", command: "touch /tmp/ai-grant-probe"}
	providerServer := httptest.NewServer(script)
	t.Cleanup(providerServer.Close)
	if _, err := profileManager.Save(adminA, profiles.Profile{
		BaseURL: providerServer.URL, APIKey: "test-key", Model: "test-model",
		ContextWindow: 32768, Stream: true,
	}); err != nil {
		t.Fatal(err)
	}

	terminalTransport := &outcomeFakeTransport{}
	sessions := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return terminalTransport, nil
		}),
	})
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
		CredID: &credentialID, OptionsJSON: string(optionsJSON), Tags: "env:production",
	})
	if err != nil {
		t.Fatal(err)
	}
	terminalAsset, err := database.AssetCreate(adminA, store.AssetInput{Kind: "ssh", Name: "terminal-a"})
	if err != nil {
		t.Fatal(err)
	}
	terminalConnected, err := sessions.Connect(adminA, session.Asset{ID: terminalAsset.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatalf("connect terminal asset: %v", err)
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
	dbScope := tools.Scope{ConnID: connected.ConnID}
	mixedScope := tools.Scope{SessionID: terminalConnected.Info().ID, ConnID: connected.ConnID}
	startChat := func(ctx context.Context, scope tools.Scope) (*agent.SliceStream, agent.StartResponse, error) {
		t.Helper()
		stream := &agent.SliceStream{}
		response, err := services.Agent.Start(ctx, agent.ChatArgs{Message: "run it", Scope: scope}, agent.StaticStream(stream))
		return stream, response, err
	}
	confirm := func(t *testing.T, stream *agent.SliceStream, response agent.StartResponse, decision string, environmentNames []string) agent.Event {
		t.Helper()
		confirmation := waitOutcomeEvent(t, stream, "confirmRequired")
		if err := services.Agent.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: decision, EnvironmentNames: environmentNames}); err != nil {
			t.Fatal(err)
		}
		return confirmation
	}
	pendingAsset := func(t *testing.T, response agent.StartResponse) string {
		t.Helper()
		snapshot, err := services.Agent.HITLSnapshot(response.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Pending) != 1 {
			t.Fatalf("pending interrupts = %+v", snapshot.Pending)
		}
		return snapshot.Pending[0].AssetID
	}

	// No grant: the write ruling needs confirmation on the production database,
	// and denying it leaves the database untouched.
	stream, response, err := startChat(adminA, dbScope)
	if err != nil {
		t.Fatalf("start ungranted chat: %v", err)
	}
	confirmation := confirm(t, stream, response, "deny", []string{"production"})
	if len(confirmation.RequiredEnvironmentNames) != 1 || confirmation.RequiredEnvironmentNames[0] != "production" {
		t.Fatalf("ungranted confirmRequired names = %v", confirmation.RequiredEnvironmentNames)
	}
	waitOutcomeClosed(t, stream)
	if probeExists() {
		t.Fatal("denied confirmation executed the database write")
	}

	// A permanent db_query grant created through the typed IPC authorizes the
	// same write without another confirmation — even on the production asset.
	if granted := dispatch(adminA, "ai_grant_set", `{"deviceId":"`+asset.ID+`","kinds":["db_query"]}`); !granted.OK {
		t.Fatalf("ai_grant_set = %+v", granted.Error)
	}
	stream, _, err = startChat(adminA, dbScope)
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
	stream, response, err = startChat(adminA, dbScope)
	if err != nil {
		t.Fatalf("start revoked chat: %v", err)
	}
	confirm(t, stream, response, "deny", []string{"production"})
	waitOutcomeClosed(t, stream)

	// Mixed terminal+database scope: the database confirmation binds the
	// connection's real production DB asset (never the session asset) and
	// challenges for that asset's environment names.
	stream, response, err = startChat(adminA, mixedScope)
	if err != nil {
		t.Fatalf("start mixed-scope db chat: %v", err)
	}
	confirmation = waitOutcomeEvent(t, stream, "confirmRequired")
	if assetID := pendingAsset(t, response); assetID != asset.ID {
		t.Fatalf("mixed-scope db evidence = %q, want the real DB asset %s", assetID, asset.ID)
	}
	if len(confirmation.RequiredEnvironmentNames) != 1 || confirmation.RequiredEnvironmentNames[0] != "production" {
		t.Fatalf("mixed-scope db confirmRequired names = %v", confirmation.RequiredEnvironmentNames)
	}
	if err := services.Agent.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "deny", EnvironmentNames: []string{"production"}}); err != nil {
		t.Fatal(err)
	}
	waitOutcomeClosed(t, stream)

	// The same mixed scope keeps the session asset for a terminal tool.
	script.setExecMode(true)
	stream, response, err = startChat(adminA, mixedScope)
	if err != nil {
		t.Fatalf("start mixed-scope terminal chat: %v", err)
	}
	confirmation = waitOutcomeEvent(t, stream, "confirmRequired")
	if assetID := pendingAsset(t, response); assetID != terminalAsset.ID {
		t.Fatalf("mixed-scope terminal evidence = %q, want the session asset %s", assetID, terminalAsset.ID)
	}
	if len(confirmation.RequiredEnvironmentNames) != 0 {
		t.Fatalf("terminal confirmRequired names = %v", confirmation.RequiredEnvironmentNames)
	}
	if err := services.Agent.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "deny"}); err != nil {
		t.Fatal(err)
	}
	waitOutcomeClosed(t, stream)
	script.setExecMode(false)

	// A foreign owner cannot even start a chat on owner A's connection: scope
	// validation resolves the connection server-side and rejects it.
	if _, _, err = startChat(adminB, dbScope); err == nil {
		t.Fatal("cross-owner chat started on another owner's connection")
	} else {
		var appErr *ipc.Error
		if !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
			t.Fatalf("cross-owner chat error = %v, want forbidden", err)
		}
	}

	// A connection that disappears while its confirmation is pending rejects
	// the answer instead of resuming on stale evidence.
	stream, response, err = startChat(adminA, dbScope)
	if err != nil {
		t.Fatalf("start pending-disconnect chat: %v", err)
	}
	confirmation = waitOutcomeEvent(t, stream, "confirmRequired")
	if err := service.Disconnect(adminA, connected.ConnID); err != nil {
		t.Fatal(err)
	}
	if err := services.Agent.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow", EnvironmentNames: []string{"production"}}); err == nil {
		t.Fatal("answer resumed after the connection disappeared")
	}

	// The persisted scope is stale now: the next chat fails validation instead
	// of silently keeping the grant authority.
	if _, _, err = startChat(adminA, dbScope); err == nil {
		t.Fatal("chat started on a disconnected (stale) connection")
	}
}
