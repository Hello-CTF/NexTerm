package takeover

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
)

func TestR4UserWriteAndEnterAreAtomic(t *testing.T) {
	var mu sync.Mutex
	var events []string
	aiWrites := 0
	enterStarted := make(chan struct{}, 2)
	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
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
		WriteUser: func(context.Context, string, []byte) error {
			close(writeStarted)
			<-releaseWrite
			mu.Lock()
			events = append(events, "user")
			mu.Unlock()
			return nil
		},
		Inject: func(_ context.Context, _ string, data []byte) error {
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
	userDone := make(chan error, 1)
	go func() { userDone <- manager.UserWrite(context.Background(), "tab", []byte("x")) }()
	<-writeStarted
	enterDone := make(chan error, 1)
	go func() {
		_, err := manager.Enter(context.Background(), "tab")
		enterDone <- err
	}()
	<-enterStarted
	select {
	case err := <-enterDone:
		t.Fatalf("Enter completed before the user write: %v", err)
	default:
	}
	mu.Lock()
	for _, event := range events {
		if event == "enter" {
			t.Fatalf("new ownership/banner appeared before user write: %v", events)
		}
	}
	mu.Unlock()
	close(releaseWrite)
	for _, done := range []chan error{userDone, enterDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent Enter/UserWrite deadlocked")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 3 || events[0] != "exit" || events[1] != "user" || events[2] != "enter" || aiWrites != 0 {
		t.Fatalf("ownership operation ordering = %v, AI writes = %d", events, aiWrites)
	}
}
