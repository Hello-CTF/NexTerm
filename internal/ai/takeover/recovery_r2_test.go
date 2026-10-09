package takeover

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestPauseBeforeIteratorStartupIsHonored(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	var calls atomic.Int32
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		calls.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	store := agent.NewMemoryCheckpoints()
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Checkpoints = store
	})
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	h.manager.Pause("tab")
	events := waitClosed(t, stream)
	h.mu.Lock()
	writes := len(h.aiWrites)
	h.mu.Unlock()
	if writes != 0 {
		t.Fatalf("write after pre-startup pause: %q", h.aiWrites)
	}
	if calls.Load() != 0 {
		t.Fatalf("model ran after pre-startup pause: %d calls", calls.Load())
	}
	done, failed := counts(events)
	if done != 1 || failed != 0 || events[len(events)-1].Answer != "用户暂停" {
		t.Fatalf("pre-startup pause did not stop cleanly: %+v", events)
	}
}

type ctxCheckpointStore struct {
	mu       sync.Mutex
	values   map[string][]byte
	setCalls map[string]int
	setErr   func(key string, call int) error
}

func newCtxCheckpointStore() *ctxCheckpointStore {
	return &ctxCheckpointStore{values: make(map[string][]byte), setCalls: make(map[string]int)}
}

func (s *ctxCheckpointStore) Get(ctx context.Context, id string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[id]
	return append([]byte(nil), value...), ok, nil
}

func (s *ctxCheckpointStore) Set(ctx context.Context, id string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.setCalls[id]++
	call := s.setCalls[id]
	s.mu.Unlock()
	if s.setErr != nil {
		if err := s.setErr(id, call); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.values[id] = append([]byte(nil), data...)
	s.mu.Unlock()
	return nil
}

func (s *ctxCheckpointStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.values, id)
	s.mu.Unlock()
	return nil
}

func execSetFailer(failCall int) func(string, int) error {
	return func(key string, call int) error {
		if strings.HasPrefix(key, "takeover:exec:") && call == failCall {
			return errors.New("checkpoint store unavailable")
		}
		return nil
	}
}

func TestWriteDoneSaveFailureLeavesUncertainAttempt(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`)),
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`)),
		toolCallMessage(doneCall("finished")),
	)
	store := newCtxCheckpointStore()
	store.setErr = execSetFailer(2)
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Checkpoints = store
	})
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	final := waitClosed(t, stream)
	var results []string
	for _, event := range final {
		if event.Type == "toolResult" && event.ID == "keys" {
			results = append(results, event.Text)
		}
	}
	if len(results) != 2 || !strings.Contains(results[0], "记录失败") || !strings.Contains(results[1], "结果未知") {
		t.Fatalf("results = %q", results)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 {
		t.Fatalf("uncertain replay duplicated write: %q", h.aiWrites)
	}
}

func TestWriteErrorLeavesUncertainAttempt(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`)),
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`)),
		toolCallMessage(doneCall("finished")),
	)
	store := newCtxCheckpointStore()
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Checkpoints = store
	})
	h.writeAI = func([]byte) error {
		return errors.New("connection reset after partial write")
	}
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	final := waitClosed(t, stream)
	var results []string
	for _, event := range final {
		if event.Type == "toolResult" && event.ID == "keys" {
			results = append(results, event.Text)
		}
	}
	if len(results) != 2 || !strings.Contains(results[0], "connection reset") || !strings.Contains(results[1], "结果未知") {
		t.Fatalf("results = %q", results)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 {
		t.Fatalf("uncertain replay duplicated write: %q", h.aiWrites)
	}
}

func TestSlowWriteKeepsRecordContext(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`)),
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`)),
		toolCallMessage(doneCall("finished")),
	)
	store := newCtxCheckpointStore()
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Checkpoints = store
	})
	h.writeAI = func([]byte) error {
		time.Sleep(100 * time.Millisecond)
		return nil
	}
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	final := waitClosed(t, stream)
	var results []string
	for _, event := range final {
		if event.Type == "toolResult" && event.ID == "keys" {
			results = append(results, event.Text)
		}
	}
	if len(results) != 2 || results[0] != "已发送" || results[1] != "已发送" {
		t.Fatalf("results = %q", results)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 {
		t.Fatalf("done replay duplicated write: %q", h.aiWrites)
	}
}

func TestRunFailsWhenAssetResolutionFails(t *testing.T) {
	chat := sequence(toolCallMessage(doneCall("finished")))
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.TabAsset = func(context.Context, string) (string, error) {
			return "", errors.New("tab asset unavailable")
		}
	})
	stream := &agent.SliceStream{}
	if _, err := h.manager.Run(context.Background(), RunArgs{TabID: "tab", Instruction: "go"}, agent.StaticStream(stream)); err == nil {
		t.Fatal("run started without resolved asset")
	}
}

func TestResumeRejectsEmptyRecordedAsset(t *testing.T) {
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
	restarted := NewManager(h.manager.deps)
	token, err := restarted.Enter(context.Background(), "tab")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: token, JobID: response.JobID}, agent.StaticStream(&agent.SliceStream{})); !errors.Is(err, ErrResumeMismatch) {
		t.Fatalf("resume with empty recorded asset error = %v", err)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
}

type pausedBlockStream struct {
	*agent.SliceStream
	paused  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *pausedBlockStream) Send(ctx context.Context, event agent.Event) error {
	if event.Type == "paused" {
		s.once.Do(func() { close(s.paused) })
		<-s.release
	}
	return s.SliceStream.Send(ctx, event)
}

func TestResumeWaitsForPausedFinalization(t *testing.T) {
	var calls atomic.Int32
	modelRelease := make(chan struct{})
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		switch calls.Add(1) {
		case 1:
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`))}), nil
		case 2:
			select {
			case <-modelRelease:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(doneCall("finished"))}), nil
	}}
	h := newHarness(t, chat)
	stream := &pausedBlockStream{SliceStream: &agent.SliceStream{}, paused: make(chan struct{}), release: make(chan struct{})}
	response, err := h.manager.Run(context.Background(), RunArgs{TabID: "tab", Instruction: "go"}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	_ = waitEvent(t, stream.SliceStream, "toolResult")
	h.manager.Pause("tab")
	close(modelRelease)
	<-stream.paused
	if _, err := h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}, agent.StaticStream(&agent.SliceStream{})); !errors.Is(err, ErrOwnershipActive) {
		t.Fatalf("resume during paused finalization error = %v", err)
	}
	close(stream.release)
	resumed := &agent.SliceStream{}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}, agent.StaticStream(resumed))
		if err == nil {
			break
		}
		if !errors.Is(err, ErrOwnershipActive) || time.Now().After(deadline) {
			t.Fatalf("resume after finalization: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	final := waitClosed(t, resumed)
	for _, event := range final {
		if event.Type == "paused" {
			t.Fatal("stale paused event delivered to resumed stream")
		}
	}
	if done, failed := counts(final); done != 1 || failed != 0 {
		t.Fatalf("resumed events = %+v", final)
	}
	old, oldClosed := stream.SliceStream.Snapshot()
	if !oldClosed {
		t.Fatal("old stream left open after resume")
	}
	pausedSeen := false
	for _, event := range old {
		pausedSeen = pausedSeen || event.Type == "paused"
	}
	if !pausedSeen {
		t.Fatal("paused event missing from old stream")
	}
}

type gatedCheckpointStore struct {
	inner       *ctxCheckpointStore
	gate        chan struct{}
	key         string
	blocking    atomic.Bool
	getBlocking atomic.Bool
}

func (s *gatedCheckpointStore) Get(ctx context.Context, id string) ([]byte, bool, error) {
	if id == s.key && s.getBlocking.Load() {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	return s.inner.Get(ctx, id)
}

func (s *gatedCheckpointStore) Set(ctx context.Context, id string, data []byte) error {
	if id == s.key && s.blocking.Load() {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.inner.Set(ctx, id, data)
}

func (s *gatedCheckpointStore) Delete(ctx context.Context, id string) error {
	return s.inner.Delete(ctx, id)
}

func TestPauseWaitsForCheckpointPersistence(t *testing.T) {
	var calls atomic.Int32
	modelRelease := make(chan struct{})
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`))}), nil
		}
		select {
		case <-modelRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(doneCall("finished"))}), nil
	}}
	store := &gatedCheckpointStore{inner: newCtxCheckpointStore(), gate: make(chan struct{})}
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Checkpoints = store
	})
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	_ = waitEvent(t, stream, "toolResult")
	store.key = response.JobID
	store.blocking.Store(true)
	h.manager.Pause("tab")
	time.Sleep(100 * time.Millisecond)
	events, _ := stream.Snapshot()
	for _, event := range events {
		if event.Type == "paused" {
			t.Fatal("paused published before checkpoint persisted")
		}
	}
	if _, err := h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}, agent.StaticStream(&agent.SliceStream{})); !errors.Is(err, ErrOwnershipActive) {
		t.Fatalf("resume before checkpoint persisted error = %v", err)
	}
	close(store.gate)
	close(modelRelease)
	waitEvent(t, stream, "paused")
	resumed := &agent.SliceStream{}
	if _, err := h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}, agent.StaticStream(resumed)); err != nil {
		t.Fatalf("resume after checkpoint persisted: %v", err)
	}
	final := waitClosed(t, resumed)
	if done, failed := counts(final); done != 1 || failed != 0 {
		t.Fatalf("resumed events = %+v", final)
	}
}

func TestPauseEscalationFailsClosed(t *testing.T) {
	stuck := make(chan struct{})
	secondCallStarted := make(chan struct{})
	var calls atomic.Int32
	var secondOnce sync.Once
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`))}), nil
		}
		secondOnce.Do(func() { close(secondCallStarted) })
		<-stuck
		return nil, errors.New("unreachable")
	}}
	store := agent.NewMemoryCheckpoints()
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Checkpoints = store
		deps.PauseEscalationTimeout = 50 * time.Millisecond
	})
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	_ = waitEvent(t, stream, "toolResult")
	<-secondCallStarted
	h.manager.Pause("tab")
	events := waitClosed(t, stream)
	var terminal *agent.Event
	for i := range events {
		if events[i].Type == "paused" {
			t.Fatal("paused published on timeout escalation")
		}
		if events[i].Type == "error" || events[i].Type == "done" {
			terminal = &events[i]
		}
	}
	if terminal == nil || terminal.Type != "error" || !strings.Contains(terminal.Message, "暂停超时") {
		t.Fatalf("escalation terminal = %+v", events)
	}
	if _, found, _ := store.Get(context.Background(), recoveryKey(response.JobID)); found {
		t.Fatal("recovery record written on escalation")
	}
	if _, found, _ := store.Get(context.Background(), response.JobID); found {
		t.Fatal("checkpoint kept after fail-closed escalation")
	}
	if _, found, _ := store.Get(context.Background(), execKey(response.JobID)); found {
		t.Fatal("exec record kept after fail-closed escalation")
	}
	if _, err := h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}, agent.StaticStream(&agent.SliceStream{})); !errors.Is(err, ErrStaleOwnership) {
		t.Fatalf("resume after escalation error = %v", err)
	}
	reentered, err := h.manager.Enter(context.Background(), "tab")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: reentered, JobID: response.JobID}, agent.StaticStream(&agent.SliceStream{})); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resume after re-enter error = %v", err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.manager.CloseContext(closeCtx); err != nil {
		t.Fatalf("close after escalation: %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 {
		t.Fatalf("writes = %q", h.aiWrites)
	}
}

func TestPauseWatchdogFailsClosedAndDropsLateSave(t *testing.T) {
	stuck := make(chan struct{})
	secondCallStarted := make(chan struct{})
	var calls atomic.Int32
	var secondOnce sync.Once
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`))}), nil
		}
		secondOnce.Do(func() { close(secondCallStarted) })
		<-stuck
		return nil, errors.New("unreachable")
	}}
	store := &gatedCheckpointStore{inner: newCtxCheckpointStore(), gate: make(chan struct{})}
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Checkpoints = store
		deps.PauseEscalationTimeout = 50 * time.Millisecond
	})
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	_ = waitEvent(t, stream, "toolResult")
	<-secondCallStarted
	store.key = response.JobID
	store.blocking.Store(true)
	h.manager.Pause("tab")
	events := waitClosed(t, stream)
	var terminal *agent.Event
	for i := range events {
		if events[i].Type == "paused" {
			t.Fatal("paused published by watchdog path")
		}
		if events[i].Type == "error" || events[i].Type == "done" {
			terminal = &events[i]
		}
	}
	if terminal == nil || terminal.Type != "error" || !strings.Contains(terminal.Message, "暂停超时") {
		t.Fatalf("watchdog terminal = %+v", events)
	}
	if _, found, _ := store.Get(context.Background(), recoveryKey(response.JobID)); found {
		t.Fatal("recovery record written on watchdog path")
	}
	if _, err := h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}, agent.StaticStream(&agent.SliceStream{})); !errors.Is(err, ErrStaleOwnership) {
		t.Fatalf("resume after watchdog error = %v", err)
	}
	close(store.gate)
	time.Sleep(50 * time.Millisecond)
	if _, found, _ := store.Get(context.Background(), response.JobID); found {
		t.Fatal("late checkpoint save not tombstoned")
	}
	if _, found, _ := store.Get(context.Background(), execKey(response.JobID)); found {
		t.Fatal("exec record kept after watchdog cleanup")
	}
	if err := h.manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPauseWatchdogPreemptsLateDefer(t *testing.T) {
	release := make(chan struct{})
	secondCallStarted := make(chan struct{})
	var calls atomic.Int32
	var secondOnce sync.Once
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`))}), nil
		}
		secondOnce.Do(func() { close(secondCallStarted) })
		select {
		case <-release:
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(doneCall("finished"))}), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	store := &gatedCheckpointStore{inner: newCtxCheckpointStore(), gate: make(chan struct{})}
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Checkpoints = store
		deps.PauseEscalationTimeout = 50 * time.Millisecond
	})
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	_ = waitEvent(t, stream, "toolResult")
	<-secondCallStarted
	store.key = response.JobID
	store.getBlocking.Store(true)
	h.manager.Pause("tab")
	close(release)
	events := waitClosed(t, stream)
	var terminal *agent.Event
	for i := range events {
		if events[i].Type == "paused" {
			t.Fatal("paused published after watchdog preempted defer")
		}
		if events[i].Type == "error" || events[i].Type == "done" {
			terminal = &events[i]
		}
	}
	if terminal == nil || terminal.Type != "error" || !strings.Contains(terminal.Message, "暂停超时") {
		t.Fatalf("preempt terminal = %+v", events)
	}
	if _, found, _ := store.Get(context.Background(), recoveryKey(response.JobID)); found {
		t.Fatal("recovery record written after watchdog preempt")
	}
	if _, err := h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}, agent.StaticStream(&agent.SliceStream{})); !errors.Is(err, ErrStaleOwnership) {
		t.Fatalf("resume after preempt error = %v", err)
	}
	close(store.gate)
	if err := h.manager.Close(); err != nil {
		t.Fatal(err)
	}
}
