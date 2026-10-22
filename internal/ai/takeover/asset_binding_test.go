package takeover

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type mutableAsset struct {
	mu    sync.Mutex
	asset string
}

func (m *mutableAsset) resolve(context.Context, string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.asset, nil
}

func (m *mutableAsset) set(asset string) {
	m.mu.Lock()
	m.asset = asset
	m.mu.Unlock()
}

func TestConfirmBoundToJobAssetIdentity(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)),
		toolCallMessage(doneCall("finished")),
	)
	asset := &mutableAsset{asset: "asset-a"}
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.TabAsset = asset.resolve
	})
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	confirmation := waitEvent(t, stream, "confirmRequired")
	args := agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}

	asset.set("asset-b")
	if err := h.manager.Confirm(args); !errors.Is(err, ErrResumeMismatch) {
		t.Fatalf("cross-asset confirm error = %v", err)
	}
	h.mu.Lock()
	writes := len(h.aiWrites)
	h.mu.Unlock()
	if writes != 0 {
		t.Fatal("cross-asset confirmation wrote the terminal")
	}

	asset.set("asset-a")
	if err := h.manager.Confirm(args); err != nil {
		t.Fatalf("confirm after asset restored: %v", err)
	}
	waitClosed(t, stream)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 || string(h.aiWrites[0]) != "touch /tmp/x\r" {
		t.Fatalf("writes = %q", h.aiWrites)
	}
}

func TestAssetChangeBlocksGrantCoveredWrite(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("one", "send_keys", `{"keys":"touch /tmp/one","enter":true}`)),
		toolCallMessage(namedToolCall("two", "send_keys", `{"keys":"touch /tmp/two","enter":true}`)),
		toolCallMessage(doneCall("finished")),
	)
	asset := &mutableAsset{asset: "asset-a"}
	settings := &grantSettings{values: make(map[string]string)}
	grants, err := guard.NewGrants(context.Background(), settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grants.Grant(context.Background(), "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Grants = grants
		deps.TabAsset = asset.resolve
	})
	h.writeAI = func([]byte) error {
		asset.set("asset-b")
		return nil
	}
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	waitEvent(t, stream, "toolResult")
	events := waitClosed(t, stream)
	done, failed := counts(events)
	if done != 0 || failed != 1 {
		t.Fatalf("grant-covered write on changed asset did not fail closed: %+v", events)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 || string(h.aiWrites[0]) != "touch /tmp/one\r" {
		t.Fatalf("writes = %q", h.aiWrites)
	}
}

func TestResumePausedRejectsChangedAsset(t *testing.T) {
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
	asset := &mutableAsset{asset: "asset-a"}
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.TabAsset = asset.resolve
	})
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	waitEvent(t, stream, "toolResult")
	h.manager.Pause("tab")
	close(release)
	waitEvent(t, stream, "paused")

	asset.set("asset-b")
	if _, err := h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}, agent.StaticStream(&agent.SliceStream{})); !errors.Is(err, ErrResumeMismatch) {
		t.Fatalf("asset mismatch resume error = %v", err)
	}
	asset.set("asset-a")
	waitTakeoverPauseReady(t, h.manager, response.JobID)
	resumed := &agent.SliceStream{}
	if _, err := h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}, agent.StaticStream(resumed)); err != nil {
		t.Fatalf("resume after asset restored: %v", err)
	}
	final := waitClosed(t, resumed)
	if done, failed := counts(final); done != 1 || failed != 0 {
		t.Fatalf("resume events = %+v", final)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 {
		t.Fatalf("resume duplicated writes: %q", h.aiWrites)
	}
}

type pausedAssetStream struct {
	*agent.SliceStream
	paused  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *pausedAssetStream) Send(ctx context.Context, event agent.Event) error {
	if err := s.SliceStream.Send(ctx, event); err != nil {
		return err
	}
	if event.Type != "paused" {
		return nil
	}
	s.once.Do(func() { close(s.paused) })
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestResumeAssetMismatchPrecedenceDuringPause(t *testing.T) {
	var calls atomic.Int32
	modelRelease := make(chan struct{})
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`))}), nil
		}
		select {
		case <-modelRelease:
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(doneCall("finished"))}), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	asset := &mutableAsset{asset: "asset-a"}
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.TabAsset = asset.resolve
	})
	stream := &pausedAssetStream{SliceStream: &agent.SliceStream{}, paused: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	releasePause := func() { releaseOnce.Do(func() { close(stream.release) }) }
	defer releasePause()
	response, err := h.manager.Run(context.Background(), RunArgs{TabID: "tab", Instruction: "go"}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	waitEvent(t, stream.SliceStream, "toolResult")
	resumeArgs := ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}
	factory := agent.StaticStream(&agent.SliceStream{})

	asset.set("asset-b")
	if _, err := h.manager.Resume(context.Background(), resumeArgs, factory); !errors.Is(err, ErrOwnershipActive) {
		t.Fatalf("active job asset mismatch resume error = %v", err)
	}
	asset.set("asset-a")
	h.manager.Pause("tab")
	close(modelRelease)
	<-stream.paused

	asset.set("asset-b")
	if _, err := h.manager.Resume(context.Background(), resumeArgs, factory); !errors.Is(err, ErrResumeMismatch) {
		t.Fatalf("paused asset mismatch resume error = %v", err)
	}
	asset.set("asset-a")
	if _, err := h.manager.Resume(context.Background(), resumeArgs, factory); !errors.Is(err, ErrOwnershipActive) {
		t.Fatalf("same-asset resume during pause finalization error = %v", err)
	}
	releasePause()
	waitTakeoverPauseReady(t, h.manager, response.JobID)
}

func TestGuardModesStillGateWithAssetBinding(t *testing.T) {
	modes := []struct {
		name        string
		mode        guard.Mode
		wantConfirm bool
		wantWrite   bool
		wantDenied  bool
	}{
		{name: "read_write asks", mode: guard.ReadWrite, wantConfirm: true, wantWrite: true},
		{name: "read_only denies", mode: guard.ReadOnly, wantDenied: true},
		{name: "silent allows", mode: guard.Silent, wantWrite: true},
		{name: "unattended denies", mode: guard.Unattended, wantDenied: true},
	}
	for _, test := range modes {
		t.Run(test.name, func(t *testing.T) {
			chat := sequence(
				toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)),
				toolCallMessage(doneCall("finished")),
			)
			h := newHarnessWith(t, chat, func(deps *Dependencies) {
				deps.Permission = func(context.Context) (guard.Config, error) {
					return guard.Config{Mode: test.mode}, nil
				}
				deps.TabAsset = func(context.Context, string) (string, error) { return "asset-a", nil }
			})
			stream := &agent.SliceStream{}
			response := h.run(t, stream, RunArgs{})
			if test.wantConfirm {
				confirmation := waitEvent(t, stream, "confirmRequired")
				if err := h.manager.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
					t.Fatal(err)
				}
			}
			events := waitClosed(t, stream)
			done, failed := counts(events)
			if done != 1 || failed != 0 {
				t.Fatalf("events = %+v", events)
			}
			asked := false
			denied := false
			for _, event := range events {
				if event.Type == "confirmRequired" {
					asked = true
				}
				if event.Type == "toolResult" && !event.OK {
					denied = true
				}
			}
			if asked != test.wantConfirm {
				t.Fatalf("confirmation asked = %v, want %v", asked, test.wantConfirm)
			}
			if denied != test.wantDenied {
				t.Fatalf("denied tool result = %v, want %v", denied, test.wantDenied)
			}
			h.mu.Lock()
			writes := len(h.aiWrites)
			h.mu.Unlock()
			if test.wantWrite && writes != 1 {
				t.Fatalf("writes = %d, want 1", writes)
			}
			if !test.wantWrite && writes != 0 {
				t.Fatalf("writes = %d, want 0", writes)
			}
		})
	}
}

func TestCancelledJobRejectsLateConfirmWithAssetBinding(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)),
		toolCallMessage(doneCall("finished")),
	)
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.TabAsset = func(context.Context, string) (string, error) { return "asset-a", nil }
	})
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	confirmation := waitEvent(t, stream, "confirmRequired")
	if err := h.manager.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, stream)
	err := h.manager.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("late confirm after cancel error = %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 0 {
		t.Fatalf("cancelled job wrote terminal: %q", h.aiWrites)
	}
}

func TestCrossOwnerConfirmLeavesPendingIntact(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)),
		toolCallMessage(doneCall("finished")),
	)
	h := newHarness(t, chat)
	alice := ipc.WithUserID(context.Background(), "account-a")
	bob := ipc.WithUserID(context.Background(), "account-b")
	token, err := h.manager.Enter(alice, "tab")
	if err != nil {
		t.Fatal(err)
	}
	stream := &agent.SliceStream{}
	run, err := h.manager.Run(alice, RunArgs{TabID: "tab", Token: token, Instruction: "go"}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")
	args := agent.Confirmation{JobID: run.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}
	if err := h.manager.ConfirmContext(bob, args); !errors.Is(err, ErrStaleOwnership) {
		t.Fatalf("cross-owner confirm error = %v", err)
	}
	if err := h.manager.ConfirmContext(alice, args); err != nil {
		t.Fatalf("owner confirm after cross-owner attempt: %v", err)
	}
	waitClosed(t, stream)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 {
		t.Fatalf("writes = %q", h.aiWrites)
	}
}
