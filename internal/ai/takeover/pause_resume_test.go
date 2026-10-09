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
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestUserPauseKeepsCheckpointAndResumes(t *testing.T) {
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
	store := agent.NewMemoryCheckpoints()
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Checkpoints = store
	})
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	waitEvent(t, stream, "toolResult")
	h.manager.Pause("tab")
	close(release)
	waitEvent(t, stream, "paused")
	if _, found, err := store.Get(context.Background(), response.JobID); err != nil || !found {
		t.Fatalf("checkpoint deleted by pause: found=%v err=%v", found, err)
	}
	events, _ := stream.Snapshot()
	if done, failed := counts(events); done+failed != 0 {
		t.Fatalf("pause produced terminal events: %+v", events)
	}
	resumed := &agent.SliceStream{}
	result, err := h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}, agent.StaticStream(resumed))
	if err != nil {
		t.Fatal(err)
	}
	if result.JobID != response.JobID {
		t.Fatalf("resume job = %s, want %s", result.JobID, response.JobID)
	}
	final := waitClosed(t, resumed)
	if done, failed := counts(final); done != 1 || failed != 0 || final[len(final)-1].Answer != "finished" {
		t.Fatalf("resume events = %+v", final)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 {
		t.Fatalf("resume duplicated writes: %q", h.aiWrites)
	}
}

func TestPauseResumeAcrossRestart(t *testing.T) {
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
	store := agent.NewMemoryCheckpoints()
	var asset atomic.Value
	asset.Store("asset-a")
	var writes atomic.Int32
	deps := Dependencies{
		Model:       func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Permission:  func(context.Context) (guard.Config, error) { return guard.Config{Mode: guard.ReadWrite}, nil },
		Checkpoints: store,
		Snapshot: func(context.Context, string) (tools.Screen, error) {
			return tools.Screen{Text: "$ ", Tail: []string{"$ "}, IdleMS: 301, CursorCol: 2}, nil
		},
		WriteAI:      func(context.Context, string, []byte) error { writes.Add(1); return nil },
		TabAsset:     func(context.Context, string) (string, error) { return asset.Load().(string), nil },
		PollInterval: time.Millisecond,
	}
	first := NewManager(deps)
	stream := &agent.SliceStream{}
	response, err := first.Run(context.Background(), RunArgs{TabID: "tab", Instruction: "go"}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	waitEvent(t, stream, "toolResult")
	first.Pause("tab")
	close(release)
	waitEvent(t, stream, "paused")
	if _, found, err := store.Get(context.Background(), recoveryKey(response.JobID)); err != nil || !found {
		t.Fatalf("recovery record missing after pause: found=%v err=%v", found, err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := first.CloseContext(closeCtx); err != nil {
		t.Fatal(err)
	}
	if _, closed := stream.Snapshot(); !closed {
		t.Fatal("stream left open after shutdown")
	}

	second := NewManager(deps)
	token, err := second.Enter(context.Background(), "tab")
	if err != nil {
		t.Fatal(err)
	}
	otherToken, err := second.Enter(context.Background(), "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Resume(context.Background(), ResumeArgs{TabID: "other", Token: otherToken, JobID: response.JobID}, agent.StaticStream(&agent.SliceStream{})); !errors.Is(err, ErrResumeMismatch) {
		t.Fatalf("cross-tab resume error = %v", err)
	}
	asset.Store("asset-b")
	if _, err := second.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: token, JobID: response.JobID}, agent.StaticStream(&agent.SliceStream{})); !errors.Is(err, ErrResumeMismatch) {
		t.Fatalf("asset mismatch resume error = %v", err)
	}
	asset.Store("asset-a")
	if _, err := second.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: token, JobID: "missing"}, agent.StaticStream(&agent.SliceStream{})); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown job resume error = %v", err)
	}
	resumed := &agent.SliceStream{}
	result, err := second.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: token, JobID: response.JobID}, agent.StaticStream(resumed))
	if err != nil {
		t.Fatal(err)
	}
	if result.JobID != response.JobID {
		t.Fatalf("resume job = %s, want %s", result.JobID, response.JobID)
	}
	final := waitClosed(t, resumed)
	if done, failed := counts(final); done != 1 || failed != 0 || final[len(final)-1].Answer != "finished" {
		t.Fatalf("recovered events = %+v", final)
	}
	if got := writes.Load(); got != 1 {
		t.Fatalf("recovered run duplicated writes: %d", got)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownPreservesRunningJobForRecovery(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(doneCall("finished"))}), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	store := agent.NewMemoryCheckpoints()
	deps := Dependencies{
		Model:       func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Permission:  func(context.Context) (guard.Config, error) { return guard.Config{Mode: guard.ReadWrite}, nil },
		Checkpoints: store,
		Snapshot: func(context.Context, string) (tools.Screen, error) {
			return tools.Screen{Text: "$ ", Tail: []string{"$ "}, IdleMS: 301, CursorCol: 2}, nil
		},
		WriteAI:      func(context.Context, string, []byte) error { return nil },
		TabAsset:     func(context.Context, string) (string, error) { return "asset-a", nil },
		PollInterval: time.Millisecond,
	}
	first := NewManager(deps)
	stream := &agent.SliceStream{}
	response, err := first.Run(context.Background(), RunArgs{TabID: "tab", Instruction: "go"}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(release)
	}()
	if err := first.CloseContext(closeCtx); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.Get(context.Background(), response.JobID); !found {
		t.Fatal("shutdown deleted checkpoint")
	}
	if _, found, _ := store.Get(context.Background(), recoveryKey(response.JobID)); !found {
		t.Fatal("shutdown missing recovery record")
	}
	second := NewManager(deps)
	token, err := second.Enter(context.Background(), "tab")
	if err != nil {
		t.Fatal(err)
	}
	resumed := &agent.SliceStream{}
	if _, err := second.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: token, JobID: response.JobID}, agent.StaticStream(resumed)); err != nil {
		t.Fatalf("resume after shutdown: %v", err)
	}
	_ = second.Cancel(response.JobID)
	waitClosed(t, resumed)
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestResumeRejectsMissingCheckpoint(t *testing.T) {
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
	store := agent.NewMemoryCheckpoints()
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Checkpoints = store
	})
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	waitEvent(t, stream, "toolResult")
	h.manager.Pause("tab")
	close(release)
	waitEvent(t, stream, "paused")
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.manager.CloseContext(closeCtx); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), response.JobID); err != nil {
		t.Fatal(err)
	}
	restarted := NewManager(h.manager.deps)
	token, err := restarted.Enter(context.Background(), "tab")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: token, JobID: response.JobID}, agent.StaticStream(&agent.SliceStream{})); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resume without checkpoint error = %v", err)
	}
	if _, found, _ := store.Get(context.Background(), recoveryKey(response.JobID)); found {
		t.Fatal("stale recovery record kept after missing checkpoint")
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPauseDuringWriteDoesNotDuplicateOnResume(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	writeEntered := make(chan struct{})
	var writeOnce sync.Once
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
	h := newHarness(t, chat)
	h.writeAI = func([]byte) error {
		writeOnce.Do(func() { close(writeEntered) })
		<-release
		return nil
	}
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	<-writeEntered
	h.manager.Pause("tab")
	close(release)
	waitEvent(t, stream, "paused")
	resumed := &agent.SliceStream{}
	if _, err := h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}, agent.StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	final := waitClosed(t, resumed)
	if done, failed := counts(final); done != 1 || failed != 0 {
		t.Fatalf("resume events = %+v", final)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 {
		t.Fatalf("write duplicated across pause: %q", h.aiWrites)
	}
	if len(h.audits) != 1 {
		t.Fatalf("write audits = %+v", h.audits)
	}
}

func TestPauseResumeCancelRace(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	h := newHarness(t, chat)
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	<-entered
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.manager.Pause("tab")
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}, agent.StaticStream(&agent.SliceStream{}))
		}()
	}
	wg.Wait()
	if err := h.manager.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, stream)
}
