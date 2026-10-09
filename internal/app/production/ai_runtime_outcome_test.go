package production

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/outcome"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type outcomeFakeTransport struct {
	mu       sync.Mutex
	commands []string
}

func (f *outcomeFakeTransport) Kind() string       { return "fake" }
func (f *outcomeFakeTransport) Generation() uint64 { return 1 }
func (f *outcomeFakeTransport) Exec(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
	f.mu.Lock()
	f.commands = append(f.commands, command)
	f.mu.Unlock()
	return base.ExecResult{Stdout: "ok"}, nil
}
func (f *outcomeFakeTransport) Ping(context.Context) (time.Duration, error) { return 0, nil }
func (f *outcomeFakeTransport) IsAlive() bool                               { return true }
func (f *outcomeFakeTransport) Close() error                                { return nil }

func (f *outcomeFakeTransport) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...)
}

func (f *outcomeFakeTransport) effectCalls(command string) int {
	count := 0
	for _, call := range f.calls() {
		if call == command {
			count++
		}
	}
	return count
}

type outcomeScriptServer struct {
	mu       sync.Mutex
	requests int
	command  string
}

func (s *outcomeScriptServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
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
		encoded, err := json.Marshal(tools.ExecCommandsArgs{Commands: []string{s.command}})
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
		spawnWriteToolCall(writer, "call-exec-1", "exec_commands", string(encoded))
		return
	}
	spawnWriteContent(writer, "done")
}

func composeOutcomeRuntime(t *testing.T, command string) (*ProductionServices, *store.Store, *outcomeFakeTransport, string) {
	t.Helper()
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "server"})
	if err != nil {
		t.Fatal(err)
	}
	profileManager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	script := &outcomeScriptServer{command: command}
	providerServer := httptest.NewServer(script)
	t.Cleanup(providerServer.Close)
	if _, err := profileManager.Save(ctx, profiles.Profile{
		BaseURL: providerServer.URL, APIKey: "test-key", Model: "test-model",
		ContextWindow: 32768, Stream: true,
	}); err != nil {
		t.Fatal(err)
	}

	transport := &outcomeFakeTransport{}
	sessions := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return transport, nil
		}),
	})
	connected, err := sessions.Connect(ctx, session.Asset{ID: asset.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}

	services := &ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions}
	if err := composeAIRuntime(ctx, services); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		services.closeAIRuntime()
		_ = sessions.Close()
		_ = database.Close()
	})
	return services, database, transport, connected.ID
}

func waitOutcomeEvent(t *testing.T, stream *agent.SliceStream, eventType string) agent.Event {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		events, closed := stream.Snapshot()
		for _, event := range events {
			if event.Type == eventType {
				return event
			}
		}
		if closed {
			t.Fatalf("stream closed before event %q", eventType)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for event %q", eventType)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitOutcomeClosed(t *testing.T, stream *agent.SliceStream) []agent.Event {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		events, closed := stream.Snapshot()
		if closed {
			return events
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the composed agent run")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func outcomeRecordCount(t *testing.T, database *store.Store) int {
	t.Helper()
	var count int
	if err := database.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM outcome_record`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func composedOutcomeRecord(t *testing.T, database *store.Store, key string) outcome.Record {
	t.Helper()
	sqliteStore, err := outcome.NewSQLiteStore(database.DB())
	if err != nil {
		t.Fatal(err)
	}
	record, err := sqliteStore.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("outcome record %q: %v", key, err)
	}
	return record
}

func TestComposedAIRuntimeRecordsOutcomeOnce(t *testing.T) {
	ctx := context.Background()
	services, database, transport, sessionID := composeOutcomeRuntime(t, "echo hi")

	stream := &agent.SliceStream{}
	response, err := services.Agent.Start(ctx, agent.ChatArgs{Message: "run it", Scope: tools.Scope{SessionID: sessionID}}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	events := waitOutcomeClosed(t, stream)
	answer := ""
	sawToolCall := false
	for _, event := range events {
		switch event.Type {
		case "error":
			t.Fatalf("composed agent run failed: %s", event.Message)
		case "toolCall":
			if event.Name == "exec_commands" {
				sawToolCall = true
			}
		case "toolResult":
			if !event.OK {
				t.Fatalf("tool result = %+v", event)
			}
		case "done":
			answer = event.Answer
		}
	}
	if !sawToolCall || answer != "done" {
		t.Fatalf("run events: toolCall=%v answer=%q", sawToolCall, answer)
	}
	if got := transport.effectCalls("echo hi"); got != 1 {
		t.Fatalf("effect runs = %d, want 1 (all calls: %v)", got, transport.calls())
	}

	if count := outcomeRecordCount(t, database); count != 1 {
		t.Fatalf("outcome_record rows = %d, want 1", count)
	}
	record := composedOutcomeRecord(t, database, tools.OutcomeKey(response.JobID, "call-exec-1"))
	if record.Outcome != outcome.OutcomeAccepted || record.State != outcome.ExecutionFinished || record.Kind != outcome.KindCommand {
		t.Fatalf("record = %+v", record)
	}
	if record.Audit.State != outcome.AuditPersisted {
		t.Fatalf("record audit = %+v", record.Audit)
	}
	if record.Result.ExitCode == nil || *record.Result.ExitCode != 0 {
		t.Fatalf("record exit code = %v", record.Result.ExitCode)
	}

	permission, err := services.Guard.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(tools.ExecCommandsArgs{Commands: []string{"echo hi"}})
	if err != nil {
		t.Fatal(err)
	}
	decision := guard.Decide(permission, guard.ClassifyTool("exec_commands", args, permission), guard.NewMemory())
	if decision.Action != guard.ActionAllow {
		t.Fatalf("test setup: decision = %+v", decision)
	}
	if want := tools.GuardAuthorizationID(decision); record.AuthorizationID != want {
		t.Fatalf("authorization id = %q, want the guard-decision binding %q", record.AuthorizationID, want)
	}

	outcomeAudits := composedAuditRows(t, database, store.OutcomeAuditKind)
	if len(outcomeAudits) != 1 {
		t.Fatalf("outcome audit rows = %d, want 1", len(outcomeAudits))
	}
	if !strings.Contains(outcomeAudits[0].PayloadJSON, record.AuthorizationID) {
		t.Fatalf("outcome audit payload missing the authorization binding: %s", outcomeAudits[0].PayloadJSON)
	}
	execAudits := composedAuditRows(t, database, "exec")
	if len(execAudits) != 1 {
		t.Fatalf("exec audit rows = %d, want 1", len(execAudits))
	}
}

func TestComposedAIRuntimeOutcomeBindsAskDecisionOnResume(t *testing.T) {
	ctx := context.Background()
	const command = "systemctl restart nginx"
	services, database, transport, sessionID := composeOutcomeRuntime(t, command)

	stream := &agent.SliceStream{}
	response, err := services.Agent.Start(ctx, agent.ChatArgs{Message: "run it", Scope: tools.Scope{SessionID: sessionID}}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitOutcomeEvent(t, stream, "confirmRequired")
	if confirmation.ID != "call-exec-1" || confirmation.Nonce == "" {
		t.Fatalf("confirmRequired = %+v", confirmation)
	}
	if got := transport.effectCalls(command); got != 0 {
		t.Fatalf("effect ran before confirmation: %d runs", got)
	}
	if err := services.Agent.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	events := waitOutcomeClosed(t, stream)
	for _, event := range events {
		if event.Type == "error" {
			t.Fatalf("composed agent run failed: %s", event.Message)
		}
	}
	if got := transport.effectCalls(command); got != 1 {
		t.Fatalf("effect runs = %d, want 1 (all calls: %v)", got, transport.calls())
	}

	if count := outcomeRecordCount(t, database); count != 1 {
		t.Fatalf("outcome_record rows = %d, want 1", count)
	}
	record := composedOutcomeRecord(t, database, tools.OutcomeKey(response.JobID, "call-exec-1"))
	if record.Outcome != outcome.OutcomeAccepted || record.Audit.State != outcome.AuditPersisted {
		t.Fatalf("record = %+v", record)
	}

	permission, err := services.Guard.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(tools.ExecCommandsArgs{Commands: []string{command}})
	if err != nil {
		t.Fatal(err)
	}
	decision := guard.Decide(permission, guard.ClassifyTool("exec_commands", args, permission), guard.NewMemory())
	if decision.Action != guard.ActionAsk {
		t.Fatalf("test setup: decision = %+v", decision)
	}
	if want := tools.GuardAuthorizationID(decision); record.AuthorizationID != want {
		t.Fatalf("authorization id = %q, want the ask-decision binding %q", record.AuthorizationID, want)
	}
	if rows := composedAuditRows(t, database, store.OutcomeAuditKind); len(rows) != 1 {
		t.Fatalf("outcome audit rows = %d, want 1", len(rows))
	}
}

func composedAuditRows(t *testing.T, database *store.Store, kind string) []store.AuditRow {
	t.Helper()
	rows, err := database.AuditQuery(context.Background(), store.AuditQuery{Kind: &kind})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
