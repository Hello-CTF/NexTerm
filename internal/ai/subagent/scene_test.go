package subagent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestSpawnSelectsExplicitModelProfile(t *testing.T) {
	var defaultCalls, profileCalls atomic.Int64
	chat := &testModel{step: func(_ context.Context, _ []*schema.Message, call int) (*schema.Message, error) {
		return schema.AssistantMessage("OK", nil), nil
	}}
	manager, err := NewManager(Config{
		NewModel: func(context.Context) (model.BaseChatModel, error) {
			defaultCalls.Add(1)
			return chat, nil
		},
		NewModelForProfile: func(_ context.Context, profileID string) (model.BaseChatModel, error) {
			if profileID != "profile-42" {
				t.Fatalf("unexpected profile ID %q", profileID)
			}
			profileCalls.Add(1)
			return chat, nil
		},
		MaxRunTime: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	handle, err := manager.Spawn(context.Background(), Request{Task: "use the scene profile", Scope: &Scope{}, ModelProfileID: "profile-42"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := waitResult(t, manager, handle)
	if err != nil || result.Status != StatusCompleted {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	if profileCalls.Load() != 1 || defaultCalls.Load() != 0 {
		t.Fatalf("profile calls = %d, default calls = %d", profileCalls.Load(), defaultCalls.Load())
	}
}

func TestSpawnWithoutProfileKeepsDefaultFactory(t *testing.T) {
	var defaultCalls, profileCalls atomic.Int64
	chat := &testModel{step: func(_ context.Context, _ []*schema.Message, call int) (*schema.Message, error) {
		return schema.AssistantMessage("OK", nil), nil
	}}
	manager, err := NewManager(Config{
		NewModel: func(context.Context) (model.BaseChatModel, error) {
			defaultCalls.Add(1)
			return chat, nil
		},
		NewModelForProfile: func(context.Context, string) (model.BaseChatModel, error) {
			profileCalls.Add(1)
			return chat, nil
		},
		MaxRunTime: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	handle, err := manager.Spawn(context.Background(), Request{Task: "default profile", Scope: &Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitResult(t, manager, handle); err != nil {
		t.Fatal(err)
	}
	if defaultCalls.Load() == 0 || profileCalls.Load() != 0 {
		t.Fatalf("default calls = %d, profile calls = %d", defaultCalls.Load(), profileCalls.Load())
	}
}

func TestSpawnWithProfileRequiresFactory(t *testing.T) {
	manager, err := NewManager(Config{
		NewModel: func(context.Context) (model.BaseChatModel, error) {
			return &testModel{step: func(context.Context, []*schema.Message, int) (*schema.Message, error) {
				return schema.AssistantMessage("OK", nil), nil
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	_, err = manager.Spawn(context.Background(), Request{Task: "x", Scope: &Scope{}, ModelProfileID: "profile-42"})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected configuration error, got %v", err)
	}
}

func TestWaitCancellationTimeoutIsHonest(t *testing.T) {
	gate := make(chan struct{})
	manager, err := NewManager(Config{
		NewModel: func(context.Context) (model.BaseChatModel, error) {
			return &testModel{step: func(context.Context, []*schema.Message, int) (*schema.Message, error) {
				return schema.AssistantMessage("OK", nil), nil
			}}, nil
		},
		OnFinish:   func(context.Context, Request, Result) error { <-gate; return nil },
		MaxRunTime: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := manager.Spawn(context.Background(), Request{Task: "slow persist", Scope: &Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := manager.Wait(ctx, handle)
	close(gate)
	if !errors.Is(err, ErrTerminalWaitTimeout) {
		t.Fatalf("wait err = %v, want ErrTerminalWaitTimeout", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("wait result status = %q, want completed snapshot", result.Status)
	}
	if _, err := manager.Wait(context.Background(), handle); err != nil {
		t.Fatalf("terminal wait after release: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestManagerCloseBoundedWithSlowOnFinish(t *testing.T) {
	gate := make(chan struct{})
	manager, err := NewManager(Config{
		NewModel: func(context.Context) (model.BaseChatModel, error) {
			return &testModel{step: func(context.Context, []*schema.Message, int) (*schema.Message, error) {
				return schema.AssistantMessage("OK", nil), nil
			}}, nil
		},
		OnFinish:   func(context.Context, Request, Result) error { <-gate; return nil },
		MaxRunTime: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := manager.Spawn(context.Background(), Request{Task: "slow persist", Scope: &Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- manager.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned before OnFinish finished: %v", err)
	case <-time.After(2 * time.Second):
	}
	close(gate)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	result, err := manager.Wait(context.Background(), handle)
	if !errors.Is(err, context.Canceled) || result.Status != StatusCanceled {
		t.Fatalf("result = %+v err = %v", result, err)
	}
}
