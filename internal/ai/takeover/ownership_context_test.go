package takeover

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestRunWithoutTokenAuthorizesExternalTab(t *testing.T) {
	foreign := errors.New("foreign tab")
	h := newHarnessWith(t, sequence(toolCallMessage(doneCall("unexpected"))), func(deps *Dependencies) {
		deps.Snapshot = func(ctx context.Context, _ string) (tools.Screen, error) {
			if ipc.OwnerID(ctx) != "account-a" {
				return tools.Screen{}, foreign
			}
			return tools.Screen{Text: "$ ", Tail: []string{"$ "}, IdleMS: 301}, nil
		}
	})
	bob := ipc.WithUserID(context.Background(), "account-b")
	factoryCalls := 0
	_, err := h.manager.Run(bob, RunArgs{TabID: "tab", Instruction: "go"}, func(context.Context, string, string) (agent.Stream, error) {
		factoryCalls++
		return &agent.SliceStream{}, nil
	})
	if !errors.Is(err, foreign) {
		t.Fatalf("run without token error = %v, want tab authorization failure", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("unauthorized run opened stream factory %d times", factoryCalls)
	}
	h.manager.mu.Lock()
	owners := len(h.manager.owners)
	h.manager.mu.Unlock()
	if owners != 0 {
		t.Fatal("unauthorized run created takeover ownership")
	}
}

func TestTakeoverControlsRejectCrossOwnerIncludingSuperadmin(t *testing.T) {
	entered := make(chan struct{})
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	h := newHarness(t, chat)
	alice := ipc.WithUserID(context.Background(), "account-a")
	bob := ipc.WithRole(ipc.WithUserID(context.Background(), "account-b"), "superadmin")
	token, err := h.manager.Enter(alice, "tab")
	if err != nil {
		t.Fatal(err)
	}
	stream := &agent.SliceStream{}
	run, err := h.manager.Run(alice, RunArgs{TabID: "tab", Token: token, Instruction: "go"}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if _, err := h.manager.Enter(bob, "tab"); !errors.Is(err, ErrStaleOwnership) {
		t.Fatalf("cross-owner enter error = %v", err)
	}
	for _, args := range []RunArgs{
		{TabID: "tab", Token: token, Instruction: "other"},
		{TabID: "tab", Instruction: "other"},
	} {
		if _, err := h.manager.Run(bob, args, agent.StaticStream(&agent.SliceStream{})); !errors.Is(err, ErrStaleOwnership) {
			t.Fatalf("cross-owner run error = %v, args = %+v", err, args)
		}
	}
	if _, err := h.manager.Resume(bob, ResumeArgs{TabID: "tab", Token: token, JobID: run.JobID}, agent.StaticStream(&agent.SliceStream{})); !errors.Is(err, ErrStaleOwnership) {
		t.Fatalf("cross-owner resume error = %v", err)
	}
	if err := h.manager.PauseFor(bob, "tab", token); !errors.Is(err, ErrStaleOwnership) {
		t.Fatalf("cross-owner pause error = %v", err)
	}
	if err := h.manager.CancelContext(bob, run.JobID); !errors.Is(err, ErrStaleOwnership) {
		t.Fatalf("cross-owner cancel error = %v", err)
	}
	if err := h.manager.ConfirmContext(bob, agent.Confirmation{JobID: run.JobID}); !errors.Is(err, ErrStaleOwnership) {
		t.Fatalf("cross-owner confirm error = %v", err)
	}
	if err := h.manager.Exit(bob, "tab", token, "foreign"); !errors.Is(err, ErrStaleOwnership) {
		t.Fatalf("cross-owner exit error = %v", err)
	}
	if err := h.manager.CancelContext(alice, run.JobID); err != nil {
		t.Fatal(err)
	}
	_ = waitClosed(t, stream)
}

type ownerLog struct {
	mu     sync.Mutex
	values []string
}

func (l *ownerLog) add(ctx context.Context) {
	l.mu.Lock()
	l.values = append(l.values, ipc.OwnerID(ctx))
	l.mu.Unlock()
}

func (l *ownerLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.values...)
}

type checkpointOperation struct {
	kind  string
	owner string
}

type ownerCheckpointStore struct {
	inner adk.CheckPointStore
	log   ownerLog
	ops   struct {
		sync.Mutex
		values []checkpointOperation
	}
}

func newOwnerCheckpointStore() *ownerCheckpointStore {
	return &ownerCheckpointStore{inner: agent.NewMemoryCheckpoints()}
}

func (s *ownerCheckpointStore) record(kind string, ctx context.Context) {
	s.log.add(ctx)
	s.ops.Lock()
	s.ops.values = append(s.ops.values, checkpointOperation{kind: kind, owner: ipc.OwnerID(ctx)})
	s.ops.Unlock()
}

func (s *ownerCheckpointStore) Get(ctx context.Context, id string) ([]byte, bool, error) {
	s.record("get", ctx)
	return s.inner.Get(ctx, id)
}

func (s *ownerCheckpointStore) Set(ctx context.Context, id string, data []byte) error {
	s.record("set", ctx)
	return s.inner.Set(ctx, id, data)
}

func (s *ownerCheckpointStore) Delete(ctx context.Context, id string) error {
	s.record("delete", ctx)
	return s.inner.(adk.CheckPointDeleter).Delete(ctx, id)
}

func (s *ownerCheckpointStore) operations() []checkpointOperation {
	s.ops.Lock()
	defer s.ops.Unlock()
	return append([]checkpointOperation(nil), s.ops.values...)
}

func requireOwnerLog(t *testing.T, log *ownerLog, owner string, minimum int) {
	t.Helper()
	values := log.snapshot()
	if len(values) < minimum {
		t.Fatalf("owner calls = %v, want at least %d", values, minimum)
	}
	for _, value := range values {
		if value != owner {
			t.Fatalf("owner calls = %v, want only %q", values, owner)
		}
	}
}

func TestAccountContextAndSequenceSurviveRestartRecovery(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`))}), nil
		}
		select {
		case <-release:
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(doneCall("finished"))}), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	store := newOwnerCheckpointStore()
	var modelOwners, permissionOwners, auditOwners ownerLog
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Checkpoints = store
		deps.TabAsset = func(context.Context, string) (string, error) { return "asset-a", nil }
		deps.Model = func(ctx context.Context) (model.BaseChatModel, uint64, error) {
			modelOwners.add(ctx)
			return chat, 32768, nil
		}
		deps.Permission = func(ctx context.Context) (guard.Config, error) {
			permissionOwners.add(ctx)
			return guard.Config{Mode: guard.ReadWrite}, nil
		}
		deps.Audit = func(ctx context.Context, _ tools.AuditEntry) error {
			auditOwners.add(ctx)
			return nil
		}
	})
	alice := ipc.WithUserID(context.Background(), "account-a")
	bob := ipc.WithUserID(context.Background(), "account-b")
	token, err := h.manager.Enter(alice, "tab")
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(alice)
	initial := &agent.SliceStream{}
	run, err := h.manager.Run(runCtx, RunArgs{TabID: "tab", Token: token, Instruction: "go"}, agent.StaticStream(initial))
	cancelRun()
	if err != nil {
		t.Fatal(err)
	}
	_ = waitEvent(t, initial, "toolResult")
	if err := h.manager.PauseFor(alice, "tab", token); err != nil {
		t.Fatal(err)
	}
	close(release)
	paused := waitEvent(t, initial, "paused")
	waitTakeoverPauseReady(t, h.manager, run.JobID)
	record, err := h.manager.loadRecovery(alice, run.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if record.OwnerID != "account-a" || record.Seq != paused.Seq {
		t.Fatalf("recovery record = %+v, paused seq = %d", record, paused.Seq)
	}
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelClose()
	if err := h.manager.CloseContext(closeCtx); err != nil {
		t.Fatal(err)
	}

	restarted := NewManager(h.manager.deps)
	t.Cleanup(func() { _ = restarted.Close() })
	bobToken, err := restarted.Enter(bob, "tab")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Resume(bob, ResumeArgs{TabID: "tab", Token: bobToken, JobID: run.JobID}, agent.StaticStream(&agent.SliceStream{})); !errors.Is(err, ErrStaleOwnership) {
		t.Fatalf("cross-owner recovery error = %v", err)
	}
	if err := restarted.Exit(bob, "tab", bobToken, "done"); err != nil {
		t.Fatal(err)
	}
	aliceToken, err := restarted.Enter(alice, "tab")
	if err != nil {
		t.Fatal(err)
	}
	resumeArgs := ResumeArgs{TabID: "tab", Token: aliceToken, JobID: run.JobID}
	if _, err := restarted.Resume(alice, resumeArgs, func(context.Context, string, string) (agent.Stream, error) {
		return nil, errors.New("factory unavailable")
	}); err == nil {
		t.Fatal("recovery unexpectedly succeeded with a failing factory")
	}
	resumed := &agent.SliceStream{}
	recovered, err := restarted.Resume(alice, resumeArgs, agent.StaticStream(resumed))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.JobID != run.JobID {
		t.Fatalf("recovered job = %s, want %s", recovered.JobID, run.JobID)
	}
	final := waitClosed(t, resumed)
	if len(final) == 0 || final[0].Seq != paused.Seq+1 {
		t.Fatalf("first recovered event = %+v, paused seq = %d", final, paused.Seq)
	}
	for index, event := range final {
		if want := paused.Seq + uint64(index) + 1; event.Seq != want {
			t.Fatalf("recovered event %d seq = %d, want %d: %+v", index, event.Seq, want, final)
		}
	}
	requireOwnerLog(t, &modelOwners, "account-a", 2)
	requireOwnerLog(t, &permissionOwners, "account-a", 2)
	requireOwnerLog(t, &auditOwners, "account-a", 1)
	for _, operation := range store.operations() {
		if operation.owner == "" {
			t.Fatalf("checkpoint %s lost request owner: %+v", operation.kind, store.operations())
		}
		if operation.kind != "get" && operation.owner != "account-a" {
			t.Fatalf("checkpoint mutation used wrong owner: %+v", operation)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 {
		t.Fatalf("recovered writes = %q", h.aiWrites)
	}
}
