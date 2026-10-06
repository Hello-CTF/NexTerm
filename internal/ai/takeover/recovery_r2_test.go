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

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
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
	store := newCtxCheckpointStore()
	store.setErr = execSetFailer(2)
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Checkpoints = store
		deps.PauseEscalationTimeout = 50 * time.Millisecond
	})
	h.writeAI = func([]byte) error {
		writeOnce.Do(func() { close(writeEntered) })
		<-release
		return nil
	}
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	<-writeEntered
	h.manager.Pause("tab")
	time.Sleep(100 * time.Millisecond)
	close(release)
	waitEvent(t, stream, "paused")
	resumed := &agent.SliceStream{}
	if _, err := h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}, agent.StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	final := waitClosed(t, resumed)
	var replayResult string
	for _, event := range final {
		if event.Type == "toolResult" && event.ID == "keys" {
			replayResult = event.Text
		}
	}
	if !strings.Contains(replayResult, "结果未知") {
		t.Fatalf("uncertain replay result = %q", replayResult)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 {
		t.Fatalf("uncertain replay duplicated write: %q", h.aiWrites)
	}
}

func TestWriteErrorLeavesUncertainAttempt(t *testing.T) {
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
	store := newCtxCheckpointStore()
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Checkpoints = store
		deps.PauseEscalationTimeout = 50 * time.Millisecond
	})
	h.writeAI = func([]byte) error {
		writeOnce.Do(func() { close(writeEntered) })
		<-release
		return errors.New("connection reset after partial write")
	}
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	<-writeEntered
	h.manager.Pause("tab")
	time.Sleep(100 * time.Millisecond)
	close(release)
	waitEvent(t, stream, "paused")
	resumed := &agent.SliceStream{}
	if _, err := h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}, agent.StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	final := waitClosed(t, resumed)
	var replayResult string
	for _, event := range final {
		if event.Type == "toolResult" && event.ID == "keys" {
			replayResult = event.Text
		}
	}
	if !strings.Contains(replayResult, "结果未知") {
		t.Fatalf("uncertain replay result = %q", replayResult)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 {
		t.Fatalf("uncertain replay duplicated write: %q", h.aiWrites)
	}
}

func TestSlowWriteKeepsRecordContext(t *testing.T) {
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
	store := newCtxCheckpointStore()
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Checkpoints = store
		deps.PauseEscalationTimeout = 50 * time.Millisecond
	})
	h.writeAI = func([]byte) error {
		writeOnce.Do(func() { close(writeEntered) })
		<-release
		return nil
	}
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	<-writeEntered
	h.manager.Pause("tab")
	time.Sleep(100 * time.Millisecond)
	close(release)
	waitEvent(t, stream, "paused")
	resumed := &agent.SliceStream{}
	if _, err := h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}, agent.StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	final := waitClosed(t, resumed)
	var replayResult string
	for _, event := range final {
		if event.Type == "toolResult" && event.ID == "keys" {
			replayResult = event.Text
		}
	}
	if replayResult != "已发送" {
		t.Fatalf("done replay result = %q", replayResult)
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
	inner    *ctxCheckpointStore
	gate     chan struct{}
	key      string
	blocking atomic.Bool
}

func (s *gatedCheckpointStore) Get(ctx context.Context, id string) ([]byte, bool, error) {
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
