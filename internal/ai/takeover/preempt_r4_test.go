package takeover

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

func TestR4PreemptAndEnterAreAtomic(t *testing.T) {
	var mu sync.Mutex
	var events []string
	aiWrites := 0
	enterStarted := make(chan struct{}, 2)
	exitStarted := make(chan struct{})
	releaseExit := make(chan struct{})
	var exitOnce sync.Once
	deps := Dependencies{
		Snapshot: func(context.Context, string) (tools.Screen, error) {
			enterStarted <- struct{}{}
			return tools.Screen{Text: "$ ", IdleMS: 301}, nil
		},
		WriteAI: func(context.Context, string, []byte) error {
			mu.Lock()
			aiWrites++
			mu.Unlock()
			return nil
		},
		Inject: func(_ context.Context, _ string, data []byte) error {
			if strings.Contains(string(data), "接管结束") {
				exitOnce.Do(func() { close(exitStarted) })
				<-releaseExit
			}
			mu.Lock()
			if strings.Contains(string(data), "接管结束") {
				events = append(events, "exit")
			} else {
				events = append(events, "enter")
			}
			mu.Unlock()
			return nil
		},
	}
	manager := NewManager(deps)
	defer manager.Close()
	if _, err := manager.Enter(context.Background(), "tab"); err != nil {
		t.Fatal(err)
	}
	<-enterStarted
	mu.Lock()
	events = nil
	mu.Unlock()
	preemptDone := make(chan struct{})
	go func() {
		manager.Preempt("tab")
		close(preemptDone)
	}()
	<-exitStarted
	enterDone := make(chan error, 1)
	go func() {
		_, err := manager.Enter(context.Background(), "tab")
		enterDone <- err
	}()
	<-enterStarted
	select {
	case err := <-enterDone:
		t.Fatalf("Enter completed before the preempt exit injection: %v", err)
	default:
	}
	mu.Lock()
	for _, event := range events {
		if event == "enter" {
			t.Fatalf("new ownership/banner appeared before preempt finished: %v", events)
		}
	}
	mu.Unlock()
	close(releaseExit)
	select {
	case err := <-enterDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent Enter/Preempt deadlocked")
	}
	<-preemptDone
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0] != "exit" || events[1] != "enter" || aiWrites != 0 {
		t.Fatalf("ownership operation ordering = %v, AI writes = %d", events, aiWrites)
	}
}
