package ipc

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNamedEventEnvelope(t *testing.T) {
	var captured Event
	emitter := EmitterFunc(func(_ context.Context, event Event) error {
		captured = event
		return nil
	})
	payload := struct {
		Revision int `json:"revision"`
	}{Revision: 7}
	if err := Emit(context.Background(), emitter, TopicLayoutChanged, payload); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(captured)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"event":"layout://changed","payload":{"revision":7}}`; got != want {
		t.Fatalf("event = %s, want %s", got, want)
	}
}

func TestEventTopicsRemainStable(t *testing.T) {
	topics := []Topic{
		TopicSessionStatus,
		TopicTerminalExit,
		TopicTerminalThrottled,
		TopicTerminalControl,
		TopicFSProgress,
		TopicDockerStats,
		TopicAIEvent,
		TopicSyncStatus,
		TopicAppError,
		TopicLayoutChanged,
	}
	want := []string{
		"session://status",
		"terminal://exit",
		"terminal://throttled",
		"terminal://control",
		"fs://progress",
		"docker://stats",
		"ai://event",
		"sync://status",
		"app://error",
		"layout://changed",
	}
	for index := range want {
		if string(topics[index]) != want[index] {
			t.Fatalf("topic %d = %s, want %s", index, topics[index], want[index])
		}
	}
}
