package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/outcome"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestPendingSendKeysBindingAfterSameIDRecreate(t *testing.T) {
	fixture := newAtomicSessionFixture(t, false)
	ctx := trustedContext()
	execution := &Execution{
		JobID: "job-pending-episode", Registry: fixture.registry(), Scope: fixture.scope(),
		Permission: guard.Config{Mode: guard.ReadWrite}, Memory: guard.NewMemory(),
	}
	available, err := execution.Tools()
	if err != nil {
		t.Fatal(err)
	}
	const arguments = `{"keys":"touch /tmp/pending-episode","enter":true}`
	chatModel := &recoveryModel{steps: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{ID: "call-pending-episode", Type: "function", Function: schema.FunctionCall{Name: "send_keys", Arguments: arguments}}}),
		schema.AssistantMessage("finished", nil),
	}}
	chatAgent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "pending-episode", Description: "pending-episode", Instruction: "Use the tool", Model: chatModel, MaxIterations: 3,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: available, ExecuteSequentially: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := newRecoveryCheckpoints()
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{Agent: chatAgent, CheckPointStore: checkpoints})
	manager, err := hitl.NewManager(hitl.Config{Checkpoints: checkpoints, TTL: time.Hour, NewNonce: func() (string, error) { return "pending-episode-nonce", nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.RegisterRun(ctx, "run-pending-episode", "checkpoint-pending-episode"); err != nil {
		t.Fatal(err)
	}
	iterator := runner.Run(ctx, []adk.Message{schema.UserMessage("execute")}, adk.WithCheckPointID("checkpoint-pending-episode"))
	var interruptContext *adk.InterruptCtx
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			for _, candidate := range event.Action.Interrupted.InterruptContexts {
				if candidate.IsRootCause {
					interruptContext = candidate
				}
			}
		}
	}
	if interruptContext == nil {
		t.Fatal("send_keys did not interrupt for confirmation")
	}
	if err := fixture.manager.CloseTab(fixture.tabID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.OpenTab(context.Background(), session.OpenTabOptions{
		TabID: fixture.tabID, SessionID: fixture.sessionID, ClientID: "client", ChannelID: "channel", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatal(err)
	}
	replacement := fixture.transport.channelAt(1)
	request, err := manager.Interrupt(ctx, interruptContext, hitl.InterruptInput{
		RunID: "run-pending-episode", CheckpointID: "checkpoint-pending-episode", CallID: "call-pending-episode", Tool: "send_keys", Kind: hitl.KindConfirm, Parameters: []byte(arguments),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, resumed, err := manager.Resume(ctx, runner, hitl.Answer{
		ID: "answer", RunID: request.RunID, RequestID: request.ID, CheckpointID: request.CheckpointID,
		TargetID: request.TargetID, CallID: request.CallID, Nonce: request.Nonce,
		Parameters: request.Parameters, Decision: hitl.DecisionAllow,
	})
	if err != nil {
		t.Fatal(err)
	}
	for {
		event, ok := resumed.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatal(event.Err)
		}
	}
	for _, write := range replacement.successfulWrites() {
		if bytes.Contains(write, []byte("touch /tmp/pending-episode")) {
			t.Fatalf("stale confirmation wrote to the replacement tab episode: %q", write)
		}
	}
}

func TestAutomaticAuthorizationRevokeDuringAuditBarrier(t *testing.T) {
	newBlockingGrants := func(t *testing.T, action string) (*guard.Grants, chan struct{}, chan struct{}) {
		t.Helper()
		entered := make(chan struct{})
		release := make(chan struct{})
		grants, err := guard.NewGrants(context.Background(), &chatGrantSettings{values: make(map[string]string)}, func(_ context.Context, event guard.GrantAuditEvent) error {
			if event.Action == action {
				close(entered)
				<-release
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return grants, entered, release
	}

	t.Run("terminal grant", func(t *testing.T) {
		grants, entered, release := newBlockingGrants(t, "use")
		if _, err := grants.Grant(context.Background(), "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
			t.Fatal(err)
		}
		h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, &chatGrantRecorder{})
		done := make(chan error, 1)
		go func() {
			_, err := h.invoke("call-revoke-barrier", "send_keys", `{"keys":"touch /tmp/revoke-barrier","enter":true}`)
			done <- err
		}()
		<-entered
		if err := grants.Revoke(context.Background(), "asset-a"); err != nil {
			t.Fatal(err)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatalf("revoked automatic admission became a new interrupt: %v", err)
		}
		if h.writeCount() != 0 {
			t.Fatal("grant revoked before execution admission still authorized the write")
		}
	})

	t.Run("terminal rule", func(t *testing.T) {
		grants, entered, release := newBlockingGrants(t, "use_rule")
		rule, err := grants.AddRule(context.Background(), "asset-a", "send_keys", "", time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, &chatGrantRecorder{})
		done := make(chan error, 1)
		go func() {
			_, err := h.invoke("call-rule-revoke-barrier", "send_keys", `{"keys":"touch /tmp/rule-revoke-barrier","enter":true}`)
			done <- err
		}()
		<-entered
		if err := grants.RevokeRule(context.Background(), rule.ID); err != nil {
			t.Fatal(err)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatalf("revoked automatic rule admission became a new interrupt: %v", err)
		}
		if h.writeCount() != 0 {
			t.Fatal("rule revoked before execution admission still authorized the write")
		}
	})

	t.Run("database grant", func(t *testing.T) {
		grants, entered, release := newBlockingGrants(t, "use")
		if _, err := grants.Grant(context.Background(), "asset-db", []guard.GrantKind{guard.GrantKindDBQuery}); err != nil {
			t.Fatal(err)
		}
		h := newDBGrantHarness(t, grants, &chatGrantRecorder{}, Scope{ConnID: "conn-1"}, "")
		h.database.bind("conn-1", "asset-db")
		done := make(chan error, 1)
		go func() {
			_, err := h.invoke(context.Background(), "call-db-revoke-barrier", "db_query", dbWriteCall)
			done <- err
		}()
		<-entered
		if err := grants.Revoke(context.Background(), "asset-db"); err != nil {
			t.Fatal(err)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatalf("revoked automatic DB admission became a new interrupt: %v", err)
		}
		if h.database.executed() != 0 {
			t.Fatal("grant revoked before execution admission still authorized the DB query")
		}
	})
}

func TestOutcomeReplayRequiresSameExecutionBinding(t *testing.T) {
	t.Run("tab episode", func(t *testing.T) {
		fixture := newAtomicSessionFixture(t, false)
		_, ledger, _ := outcomeFixture(t)
		registry := NewRegistry(WithSession(Dependencies{Outcome: ledger, PollInterval: time.Millisecond}, fixture.manager))
		call := Call{ID: "keys-bound-episode", Name: "send_keys", Args: json.RawMessage(`{"keys":"true","enter":true}`), AuthorizationID: "guard:test"}
		ctx := trustedContext()
		if first := registry.Execute(ctx, "job-bound-episode", fixture.scope(), call, nil); !first.OK {
			t.Fatalf("first execution = %+v", first)
		}
		if err := fixture.manager.CloseTab(fixture.tabID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.manager.OpenTab(context.Background(), session.OpenTabOptions{
			TabID: fixture.tabID, SessionID: fixture.sessionID, ClientID: "client", ChannelID: "channel", Cols: 80, Rows: 24,
		}); err != nil {
			t.Fatal(err)
		}
		replacement := fixture.transport.channelAt(1)
		second := registry.Execute(ctx, "job-bound-episode", fixture.scope(), call, nil)
		if second.OK {
			t.Fatalf("old episode outcome replayed as replacement-episode success: %+v", second)
		}
		for _, write := range replacement.successfulWrites() {
			if bytes.Contains(write, []byte("true")) {
				t.Fatalf("binding mismatch repeated the write on the replacement episode: %q", write)
			}
		}
	})

	t.Run("current exec session", func(t *testing.T) {
		_, ledger, _ := outcomeFixture(t)
		terminal := newTabFake()
		terminal.bindSessionAsset("session-a", AssetIdentity{AssetID: "asset-a", ConfigFingerprint: "config-a"})
		terminal.bindSessionAsset("session-b", AssetIdentity{AssetID: "asset-b", ConfigFingerprint: "config-b"})
		transport := &fakeTransport{files: newFakeFS(), result: base.ExecResult{Stdout: "ok"}}
		registry := NewRegistry(Dependencies{
			Terminal: terminal, Outcome: ledger,
			Transport: func(context.Context, string) (base.Transport, error) { return transport, nil },
		})
		call := Call{ID: "exec-bound-session", Name: "exec_commands", Args: json.RawMessage(`{"commands":["echo hi"]}`), AuthorizationID: "guard:test"}
		if first := registry.Execute(context.Background(), "job-bound-session", Scope{SessionID: "session-a"}, call, nil); !first.OK {
			t.Fatalf("first execution = %+v", first)
		}
		second := registry.Execute(context.Background(), "job-bound-session", Scope{SessionID: "session-b"}, call, nil)
		if second.OK {
			t.Fatalf("old session outcome replayed as current-session success: %+v", second)
		}
		if transport.calls() != 1 {
			t.Fatalf("binding mismatch repeated exec: calls = %d, want 1", transport.calls())
		}
	})

	t.Run("database connection", func(t *testing.T) {
		_, ledger, _ := outcomeFixture(t)
		database := &identityDatabase{assets: map[string]string{"conn-a": "asset-a", "conn-b": "asset-b"}, owners: map[string]string{}}
		registry := NewRegistry(Dependencies{Database: database, Outcome: ledger})
		call := Call{ID: "db-bound-connection", Name: "db_query", Args: json.RawMessage(dbWriteCall), AuthorizationID: "guard:test"}
		if first := registry.Execute(context.Background(), "job-bound-db", Scope{ConnID: "conn-a"}, call, nil); !first.OK {
			t.Fatalf("first execution = %+v", first)
		}
		second := registry.Execute(context.Background(), "job-bound-db", Scope{ConnID: "conn-b"}, call, nil)
		if second.OK {
			t.Fatalf("old connection outcome replayed as current-connection success: %+v", second)
		}
		if database.executed() != 1 {
			t.Fatalf("binding mismatch repeated DB query: calls = %d, want 1", database.executed())
		}
	})
}

func TestOutcomeReplayAcceptsOnlyTheSameBoundEpisode(t *testing.T) {
	fixture := newAtomicSessionFixture(t, false)
	_, ledger, _ := outcomeFixture(t)
	registry := NewRegistry(WithSession(Dependencies{Outcome: ledger, PollInterval: time.Millisecond}, fixture.manager))
	call := Call{ID: "keys-same-episode", Name: "send_keys", Args: json.RawMessage(`{"keys":"true","enter":true}`), AuthorizationID: "guard:test"}
	ctx := trustedContext()
	if first := registry.Execute(ctx, "job-same-episode", fixture.scope(), call, nil); !first.OK {
		t.Fatalf("first execution = %+v", first)
	}
	second := registry.Execute(ctx, "job-same-episode", fixture.scope(), call, nil)
	if !second.OK || second.OperationID == "" {
		t.Fatalf("same-episode replay = %+v", second)
	}
	if writes := fixture.channel.successfulWrites(); len(writes) != 1 {
		t.Fatalf("same-episode replay repeated the write: %q", writes)
	}
}

func TestOutcomeReplayRefusesLegacyUnboundRecord(t *testing.T) {
	fixture := newAtomicSessionFixture(t, false)
	_, ledger, _ := outcomeFixture(t)
	call := Call{ID: "keys-legacy-unbound", Name: "send_keys", Args: json.RawMessage(`{"keys":"true","enter":true}`), AuthorizationID: "guard:test"}
	legacyTerminal := &fakeTerminal{screen: Screen{Text: "$ ", CursorCol: 2, Cols: 80, Rows: 24}}
	legacy := NewRegistry(Dependencies{Terminal: legacyTerminal, Outcome: ledger, PollInterval: time.Millisecond})
	if first := legacy.Execute(trustedContext(), "job-legacy-unbound", fixture.scope(), call, nil); !first.OK {
		t.Fatalf("legacy execution = %+v", first)
	}
	bound := NewRegistry(WithSession(Dependencies{Outcome: ledger, PollInterval: time.Millisecond}, fixture.manager))
	second := bound.Execute(trustedContext(), "job-legacy-unbound", fixture.scope(), call, nil)
	if second.OK {
		t.Fatalf("legacy-unbound outcome replayed as bound current-scope success: %+v", second)
	}
	if writes := fixture.channel.successfulWrites(); len(writes) != 0 {
		t.Fatalf("legacy-unbound conflict repeated the write: %q", writes)
	}
}

type admissionBarrierStore struct {
	inner   *outcome.SQLiteStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *admissionBarrierStore) Reserve(ctx context.Context, record outcome.Record) (outcome.Record, bool, error) {
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
	return s.inner.Reserve(ctx, record)
}

func (s *admissionBarrierStore) Update(ctx context.Context, record outcome.Record, revision uint64) error {
	return s.inner.Update(ctx, record, revision)
}

func (s *admissionBarrierStore) Get(ctx context.Context, key string) (outcome.Record, error) {
	return s.inner.Get(ctx, key)
}

func TestAutomaticAuthorizationRevokeAfterAdmissionKeepsAdmittedCall(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	storage, _, sqliteStore := outcomeFixture(t)
	barrier := &admissionBarrierStore{inner: sqliteStore, entered: make(chan struct{}), release: make(chan struct{})}
	ledger, err := outcome.New(outcome.Options{Store: barrier, Auditor: storage.OutcomeAuditor()})
	if err != nil {
		t.Fatal(err)
	}
	h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, recorder)
	h.execution.Registry.deps.Outcome = ledger
	done := make(chan error, 1)
	go func() {
		_, err := h.invoke("call-already-admitted", "send_keys", `{"keys":"touch /tmp/already-admitted","enter":true}`)
		done <- err
	}()
	<-barrier.entered
	if err := grants.Revoke(context.Background(), "asset-a"); err != nil {
		t.Fatal(err)
	}
	close(barrier.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if h.writeCount() != 1 {
		t.Fatalf("already-admitted call was retroactively cancelled: writes = %d, want 1", h.writeCount())
	}
}

func TestSessionExpectedEpisodeChecksRawAndPasteSubmitWrites(t *testing.T) {
	for _, paste := range []bool{false, true} {
		t.Run(fmt.Sprintf("paste=%t", paste), func(t *testing.T) {
			fixture := newAtomicSessionFixture(t, false)
			ctx := trustedContext()
			episode, ok := fixture.manager.RosterRuntimeIncarnation(ctx, fixture.tabID)
			if !ok || episode == "" {
				t.Fatal("runtime episode unavailable")
			}
			if err := fixture.manager.CloseTab(fixture.tabID); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.manager.OpenTab(context.Background(), session.OpenTabOptions{
				TabID: fixture.tabID, SessionID: fixture.sessionID, ClientID: "client", ChannelID: "channel", Cols: 80, Rows: 24,
			}); err != nil {
				t.Fatal(err)
			}
			boundCtx := session.WithExpectedTabEpisode(ctx, fixture.tabID, episode)
			adapter := SessionTerminal(fixture.manager)
			var err error
			if paste {
				err = adapter.(PasteSubmitTerminal).WritePasteSubmit(boundCtx, fixture.tabID, "touch /tmp/episode-paste", true, 0)
			} else {
				err = adapter.Write(boundCtx, fixture.tabID, []byte("touch /tmp/episode-raw"))
			}
			if !errors.Is(err, session.ErrTabEpisodeChanged) {
				t.Fatalf("write after episode replacement = %v, want ErrTabEpisodeChanged", err)
			}
			if writes := fixture.transport.channelAt(1).successfulWrites(); len(writes) != 0 {
				t.Fatalf("stale episode reached replacement channel: %q", writes)
			}
		})
	}
}

func TestOneTimeConfirmationDoesNotRevalidateAutomaticGrant(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	decision := grants.Evaluate(context.Background(), guard.Config{Mode: guard.ReadWrite}, guard.Confirm(guard.KindProcess, "write"), nil, "asset-a", "send_keys", "run")
	if decision.Authorization == nil {
		t.Fatal("automatic decision has no binding")
	}
	if err := grants.Revoke(context.Background(), "asset-a"); err != nil {
		t.Fatal(err)
	}
	h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, recorder)
	result := h.execution.Registry.Execute(context.Background(), h.execution.JobID, h.execution.Scope, Call{
		ID: "call-one-time", Name: "send_keys", Args: json.RawMessage(`{"keys":"touch /tmp/one-time","enter":true}`),
		AuthorizationID: GuardAuthorizationID(decision), Authorization: decision.Authorization, OneTimeConfirmation: true,
	}, nil)
	if !result.OK || h.writeCount() != 1 {
		t.Fatalf("one-time confirmation inherited an automatic grant revalidation: result=%+v writes=%d", result, h.writeCount())
	}
}

type legacyEpisodeTerminal struct {
	Terminal
	tabSession TabSessionTerminal
}

func (t *legacyEpisodeTerminal) TabSession(ctx context.Context, tabID string) (string, error) {
	return t.tabSession.TabSession(ctx, tabID)
}

func TestLegacyPendingSendKeysDoesNotAdoptReplacementEpisode(t *testing.T) {
	fixture := newAtomicSessionFixture(t, false)
	ctx := trustedContext()
	deps := WithSession(Dependencies{PollInterval: time.Millisecond}, fixture.manager)
	realTerminal := deps.Terminal
	deps.Terminal = &legacyEpisodeTerminal{Terminal: realTerminal, tabSession: realTerminal.(TabSessionTerminal)}
	registry := NewRegistry(deps)
	execution := &Execution{
		JobID: "job-legacy-episode", Registry: registry, Scope: fixture.scope(),
		Permission: guard.Config{Mode: guard.ReadWrite}, Memory: guard.NewMemory(),
	}
	available, err := execution.Tools()
	if err != nil {
		t.Fatal(err)
	}
	const arguments = `{"keys":"touch /tmp/legacy-episode","enter":true}`
	chatModel := &recoveryModel{steps: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{ID: "call-legacy-episode", Type: "function", Function: schema.FunctionCall{Name: "send_keys", Arguments: arguments}}}),
		schema.AssistantMessage("finished", nil),
	}}
	chatAgent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "legacy-episode", Description: "legacy-episode", Instruction: "Use the tool", Model: chatModel, MaxIterations: 3,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: available, ExecuteSequentially: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := newRecoveryCheckpoints()
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{Agent: chatAgent, CheckPointStore: checkpoints})
	manager, err := hitl.NewManager(hitl.Config{Checkpoints: checkpoints, TTL: time.Hour, NewNonce: func() (string, error) { return "legacy-episode-nonce", nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.RegisterRun(ctx, "run-legacy-episode", "checkpoint-legacy-episode"); err != nil {
		t.Fatal(err)
	}
	iterator := runner.Run(ctx, []adk.Message{schema.UserMessage("execute")}, adk.WithCheckPointID("checkpoint-legacy-episode"))
	var interruptContext *adk.InterruptCtx
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			for _, candidate := range event.Action.Interrupted.InterruptContexts {
				if candidate.IsRootCause {
					interruptContext = candidate
				}
			}
		}
	}
	if interruptContext == nil {
		t.Fatal("legacy send_keys did not interrupt")
	}
	request, err := manager.Interrupt(ctx, interruptContext, hitl.InterruptInput{
		RunID: "run-legacy-episode", CheckpointID: "checkpoint-legacy-episode", CallID: "call-legacy-episode", Tool: "send_keys", Kind: hitl.KindConfirm, Parameters: []byte(arguments),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.CloseTab(fixture.tabID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.OpenTab(context.Background(), session.OpenTabOptions{
		TabID: fixture.tabID, SessionID: fixture.sessionID, ClientID: "client", ChannelID: "channel", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatal(err)
	}
	replacement := fixture.transport.channelAt(1)

	registry.deps.Terminal = realTerminal
	restartedExecution := &Execution{
		JobID: execution.JobID, Registry: registry, Scope: execution.Scope,
		Permission: execution.Permission, Memory: execution.Memory,
	}
	restartedTools, err := restartedExecution.Tools()
	if err != nil {
		t.Fatal(err)
	}
	restartedAgent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "legacy-episode", Description: "legacy-episode", Instruction: "Use the tool",
		Model: &recoveryModel{steps: []*schema.Message{schema.AssistantMessage("finished", nil)}}, MaxIterations: 3,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: restartedTools, ExecuteSequentially: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	restartedRunner := adk.NewRunner(context.Background(), adk.RunnerConfig{Agent: restartedAgent, CheckPointStore: checkpoints})
	_, resumed, err := manager.Resume(ctx, restartedRunner, hitl.Answer{
		ID: "answer", RunID: request.RunID, RequestID: request.ID, CheckpointID: request.CheckpointID,
		TargetID: request.TargetID, CallID: request.CallID, Nonce: request.Nonce,
		Parameters: request.Parameters, Decision: hitl.DecisionAllow,
	})
	if err != nil {
		t.Fatal(err)
	}
	for {
		event, ok := resumed.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatal(event.Err)
		}
	}
	for _, write := range replacement.successfulWrites() {
		if bytes.Contains(write, []byte("touch /tmp/legacy-episode")) {
			t.Fatalf("legacy unbound confirmation adopted the replacement episode: %q", write)
		}
	}
}

func TestTerminalCreateReplayKeepsAuthorizationBinding(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	connected := false
	vacated := false
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	registry := p1Registry(fake)
	registry.deps.Outcome = ledger
	registry.deps.ResolveAssetSessionIdentity = func(context.Context, string) (string, AssetIdentity, error) {
		if !connected || vacated {
			return "", AssetIdentity{}, ErrNoLiveSession
		}
		return "s9", identityA, nil
	}
	connector := &r13Connector{sessionIDs: []string{"s9", "s10"}, onConnect: func(string) { connected = true }}
	registry.deps.AssetConnector = connector
	grants := newChatGrantManager(t, &chatGrantRecorder{})
	if _, err := grants.Grant(context.Background(), "a1", []guard.GrantKind{guard.GrantKindSessionExec}); err != nil {
		t.Fatal(err)
	}
	decision := grants.Evaluate(context.Background(), guard.Config{Mode: guard.ReadWrite}, guard.Confirm(guard.KindProcess, "create"), nil, "a1", "terminal_create", "job")
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = GuardAuthorizationID(decision)
	call.Authorization = decision.Authorization
	call.OneTimeConfirmation = true
	scope := Scope{SessionID: "s1", TabID: "t1"}
	if first := registry.Execute(context.Background(), "job", scope, call, nil); !first.OK {
		t.Fatalf("first create = %+v", first)
	}
	vacated = true
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if !second.OK || !strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("same-binding terminal_create did not replay: %+v", second)
	}
	if connector.callCount() != 1 || len(fake.created) != 1 {
		t.Fatalf("same-binding replay re-executed: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
}

func TestAuthorizationGenerationNeverReusedAcrossRestartAndSyncRewrite(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-generation")
	path := fmt.Sprintf("%s/grants.db", t.TempDir())
	database, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	newGrants := func(db *store.Store) *guard.Grants {
		manager, err := guard.NewGrants(ctx, db, nil, guard.WithShared(false))
		if err != nil {
			t.Fatal(err)
		}
		return manager
	}
	newLedger := func(db *store.Store) *outcome.Ledger {
		sqliteStore, err := outcome.NewSQLiteStore(db.GORM())
		if err != nil {
			t.Fatal(err)
		}
		ledger, err := outcome.New(outcome.Options{Store: sqliteStore, Auditor: db.OutcomeAuditor()})
		if err != nil {
			t.Fatal(err)
		}
		return ledger
	}
	terminal := newTabFake()
	terminal.bindSessionAsset("session-a", AssetIdentity{AssetID: "asset-a", ConfigFingerprint: "config-a"})
	transport := &fakeTransport{files: newFakeFS(), result: base.ExecResult{Stdout: "ok"}}
	newRegistry := func(manager *guard.Grants, ledger *outcome.Ledger) *Registry {
		return NewRegistry(Dependencies{
			Terminal: terminal, Grants: manager, Outcome: ledger,
			Transport: func(context.Context, string) (base.Transport, error) { return transport, nil },
		})
	}
	permission := guard.Config{Mode: guard.ReadWrite}
	call := p1Call("call-generation", "exec_commands", `{"commands":["touch /tmp/generation-aba"]}`)
	ruling := guard.ClassifyTool(call.Name, call.Args, permission)
	decisionFor := func(manager *guard.Grants) guard.Decision {
		return manager.EvaluateResource(ctx, permission, ruling, nil, "asset-a", call.Name, "job-generation", resourceForCall(call))
	}
	execute := func(registry *Registry, decision guard.Decision) Output {
		current := call
		current.AuthorizationID = GuardAuthorizationID(decision)
		current.Authorization = decision.Authorization
		return registry.Execute(ctx, "job-generation", Scope{SessionID: "session-a"}, current, nil)
	}
	grants := newGrants(database)
	if _, err := grants.Grant(ctx, "asset-a", []guard.GrantKind{guard.GrantKindSessionExec}); err != nil {
		t.Fatal(err)
	}
	oldDecision := decisionFor(grants)
	if first := execute(newRegistry(grants, newLedger(database)), oldDecision); !first.OK {
		t.Fatalf("first execution = %+v", first)
	}
	if transport.calls() != 1 {
		t.Fatalf("first effect calls = %d, want 1", transport.calls())
	}
	if err := grants.Revoke(ctx, "asset-a"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	grants = newGrants(database)
	if _, err := grants.Grant(ctx, "asset-a", []guard.GrantKind{guard.GrantKindSessionExec}); err != nil {
		t.Fatal(err)
	}
	if err := grants.RevalidateAuthorization(ctx, oldDecision.Authorization); err == nil {
		t.Fatal("revoked generation was admitted after restart and regrant")
	}
	newDecision := decisionFor(grants)
	if GuardAuthorizationID(newDecision) == GuardAuthorizationID(oldDecision) {
		t.Fatal("restart reused the revoked generation's authorization identity")
	}
	if second := execute(newRegistry(grants, newLedger(database)), newDecision); second.OK {
		t.Fatalf("revoked generation's outcome replayed after restart: %+v", second)
	}
	if transport.calls() != 1 {
		t.Fatalf("restart replay repeated the effect: calls = %d, want 1", transport.calls())
	}
	otherManager := newGrants(database)
	if err := otherManager.RevalidateAuthorization(ctx, newDecision.Authorization); err != nil {
		t.Fatalf("persisted generation was not shared by managers: %v", err)
	}

	raw, found, err := database.SettingGet(ctx, guard.DeviceGrantsSettingKeyForUser(ctx))
	if err != nil || !found {
		t.Fatalf("grant setting = %t, %v", found, err)
	}
	var records map[string]guard.DeviceGrant
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SettingSet(ctx, guard.DeviceGrantsSettingKeyForUser(ctx), string(encoded)); err != nil {
		t.Fatal(err)
	}
	syncedManager := newGrants(database)
	if _, err := syncedManager.Grant(ctx, "asset-a", []guard.GrantKind{guard.GrantKindSessionExec}); err != nil {
		t.Fatal(err)
	}
	if err := syncedManager.RevalidateAuthorization(ctx, newDecision.Authorization); err == nil {
		t.Fatal("sync rewrite revived the previous generation")
	}
	syncedDecision := decisionFor(syncedManager)
	if third := execute(newRegistry(syncedManager, newLedger(database)), syncedDecision); third.OK {
		t.Fatalf("sync-rewritten generation replayed the old outcome: %+v", third)
	}
	if transport.calls() != 1 {
		t.Fatalf("sync rewrite repeated the effect: calls = %d, want 1", transport.calls())
	}
}

// TestLegacyGrantRecordsAreNotMigratedDatabase pins the 1.3.0 contract on a real
// store: grant and rule records without a generation (the pre-1.3.0 format) are
// not migrated on evaluation, never authorize a call, keep their row and LWW
// timestamp untouched, and stay listed as legacy.
func TestLegacyGrantRecordsAreNotMigratedDatabase(t *testing.T) {
	for _, kind := range []string{"grant", "rule"} {
		t.Run(kind, func(t *testing.T) {
			database := dbtest.NewFixture(t).OpenStore(t)
			ctx := ipc.WithUserID(context.Background(), "owner-legacy-"+kind)
			manager, err := guard.NewGrants(ctx, database, nil, guard.WithShared(false))
			if err != nil {
				t.Fatal(err)
			}
			var key string
			switch kind {
			case "grant":
				if _, err := manager.Grant(ctx, "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
					t.Fatal(err)
				}
				key = guard.DeviceGrantsSettingKeyForUser(ctx)
			case "rule":
				if _, err := manager.AddRule(ctx, "asset-a", "write_file", "/var/log/**", time.Time{}); err != nil {
					t.Fatal(err)
				}
				key = guard.GrantRulesSettingKeyForUser(ctx)
			}
			raw, found, err := database.SettingGet(ctx, key)
			if err != nil || !found {
				t.Fatalf("%s setting = %t, %v", kind, found, err)
			}
			// Re-encoding through the exported shape drops the generation, producing the pre-1.3.0 format.
			var legacy []byte
			if kind == "grant" {
				var records map[string]guard.DeviceGrant
				if err := json.Unmarshal([]byte(raw), &records); err != nil {
					t.Fatal(err)
				}
				legacy, err = json.Marshal(records)
			} else {
				var records []guard.GrantRule
				if err := json.Unmarshal([]byte(raw), &records); err != nil {
					t.Fatal(err)
				}
				legacy, err = json.Marshal(records)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := database.SettingSet(ctx, key, string(legacy)); err != nil {
				t.Fatal(err)
			}
			readRow := func() store.Setting {
				var row store.Setting
				if err := database.GORM().WithContext(ctx).Where(store.Setting{Key: key}).First(&row).Error; err != nil {
					t.Fatal(err)
				}
				return row
			}
			before := readRow()
			manager, err = guard.NewGrants(ctx, database, nil, guard.WithShared(false))
			if err != nil {
				t.Fatal(err)
			}
			var decision guard.Decision
			if kind == "grant" {
				decision = manager.Evaluate(ctx, guard.Config{Mode: guard.ReadWrite}, guard.Confirm(guard.KindProcess, "write"), nil, "asset-a", "send_keys", "run-legacy")
			} else {
				decision = manager.EvaluateResource(ctx, guard.Config{Mode: guard.ReadWrite}, guard.Confirm(guard.KindWriteFS, "write"), nil, "asset-a", "write_file", "run-legacy", guard.Resource{Action: "write_file", Path: "/var/log/app.log"})
			}
			if decision.Action == guard.ActionAllow || decision.Authorization != nil {
				t.Fatalf("legacy %s authorized a call: %+v", kind, decision)
			}
			if after := readRow(); after.Value != before.Value || after.UpdatedAt != before.UpdatedAt {
				t.Fatalf("evaluation migrated the legacy %s: before=%+v after=%+v", kind, before, after)
			}
			if kind == "grant" {
				listed, err := manager.List(ctx)
				if err != nil || len(listed) != 1 || !listed[0].Legacy {
					t.Fatalf("legacy grant listing = %+v, %v", listed, err)
				}
			} else {
				rules, err := manager.Rules(ctx)
				if err != nil || len(rules) != 1 || !rules[0].Legacy {
					t.Fatalf("legacy rule listing = %+v, %v", rules, err)
				}
			}
		})
	}
}

func TestAuthorizationRevalidationReloadRevokeBeforeAdmission(t *testing.T) {
	for _, test := range []struct {
		name     string
		kind     string
		recorded bool
	}{
		{name: "grant_execution", kind: "grant"},
		{name: "grant_outcome", kind: "grant", recorded: true},
		{name: "rule_execution", kind: "rule"},
		{name: "rule_outcome", kind: "rule", recorded: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := dbtest.NewFixture(t).OpenStore(t)
			ctx := ipc.WithUserID(context.Background(), "owner-reload-revoke-"+test.name)
			recorder := &chatGrantRecorder{}
			var grants *guard.Grants
			var once sync.Once
			var hookErr error
			armed := false
			ruleID := ""
			manager, err := guard.NewGrants(ctx, database, recorder.record, guard.WithShared(false), guard.WithRevalidationAfterState(func() {
				if !armed {
					return
				}
				once.Do(func() {
					if err := grants.Reload(ctx); err != nil {
						hookErr = err
						return
					}
					if test.kind == "grant" {
						hookErr = grants.Revoke(ctx, "asset-a")
					} else {
						hookErr = grants.RevokeRule(ctx, ruleID)
					}
				})
			}))
			if err != nil {
				t.Fatal(err)
			}
			grants = manager
			if test.kind == "grant" {
				if _, err := grants.Grant(ctx, "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
					t.Fatal(err)
				}
			} else {
				rule, err := grants.AddRule(ctx, "asset-a", "send_keys", "", time.Time{})
				if err != nil {
					t.Fatal(err)
				}
				ruleID = rule.ID
			}
			decision := grants.Evaluate(ctx, guard.Config{Mode: guard.ReadWrite}, guard.Confirm(guard.KindProcess, "write"), nil, "asset-a", "send_keys", "job-grant-1")
			if decision.Action != guard.ActionAllow || decision.Authorization == nil {
				t.Fatalf("initial authorization = %+v", decision)
			}
			h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, recorder)
			outcomeStore, err := outcome.NewSQLiteStore(database.GORM())
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := outcome.New(outcome.Options{Store: outcomeStore, Auditor: database.OutcomeAuditor()})
			if err != nil {
				t.Fatal(err)
			}
			h.execution.Registry.deps.Outcome = ledger
			call := p1Call("call-reload-revoke-"+test.name, "send_keys", `{"keys":"touch /tmp/reload-revoke","enter":true}`)
			call.AuthorizationID = GuardAuthorizationID(decision)
			call.Authorization = decision.Authorization
			execute := func() Output {
				return h.execution.Registry.Execute(ctx, h.execution.JobID, h.execution.Scope, call, nil)
			}
			wantWrites := 0
			if test.recorded {
				if first := execute(); !first.OK {
					t.Fatalf("initial execution = %+v", first)
				}
				wantWrites = 1
			}
			armed = true
			if result := execute(); result.OK {
				t.Fatalf("Reload→Revoke still returned success (recorded=%t): %+v", test.recorded, result)
			}
			if hookErr != nil {
				t.Fatal(hookErr)
			}
			if writes := h.writeCount(); writes != wantWrites {
				t.Fatalf("writes after Reload→Revoke = %d, want %d", writes, wantWrites)
			}
			if err := grants.RevalidateAuthorization(ctx, decision.Authorization); err == nil {
				t.Fatal("final authorization revalidation accepted the revoked generation")
			}
		})
	}
}
