package production

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/logtail"
)

func newLogTailDrainSink(t *testing.T, h *hub.Hub, channelID string) *logTailJSONSink {
	t.Helper()
	raw, err := (hub.StreamFactory{Hub: h}).OpenJSON(context.Background(), ipc.ChannelRef{ID: channelID})
	if err != nil {
		t.Fatal(err)
	}
	return &logTailJSONSink{stream: ipc.NewTypedJSONStream[logtail.Event](raw), raw: raw}
}

func sendLogTailDrainEvents(t *testing.T, sink *logTailJSONSink) {
	t.Helper()
	for _, event := range []logtail.Event{
		{RunID: "drain", Sequence: 1, Kind: logtail.EventLine, SourceID: "source", Text: "last-line"},
		{RunID: "drain", Sequence: 2, Kind: logtail.EventStatus, SourceID: "source", Status: logtail.StatusFailed, Error: "start failed"},
		{RunID: "drain", Sequence: 3, Kind: logtail.EventDone},
	} {
		if err := sink.Send(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
}

func nextLogTailDrainEvent(t *testing.T, receiver *hub.Receiver) (logtail.Event, uint64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	frame, err := receiver.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var event logtail.Event
	if err := json.Unmarshal(frame.Data, &event); err != nil {
		t.Fatal(err)
	}
	return event, frame.Sequence
}

func assertLogTailDrainEvent(t *testing.T, receiver *hub.Receiver, index int) {
	t.Helper()
	event, sequence := nextLogTailDrainEvent(t, receiver)
	switch index {
	case 0:
		if event.Kind != logtail.EventLine || event.Text != "last-line" {
			t.Fatalf("first drained event = %+v", event)
		}
	case 1:
		if event.Kind != logtail.EventStatus || event.Status != logtail.StatusFailed || event.Error != "start failed" {
			t.Fatalf("failure drained event = %+v", event)
		}
	case 2:
		if event.Kind != logtail.EventDone {
			t.Fatalf("terminal drained event = %+v", event)
		}
	default:
		t.Fatalf("unexpected event index %d", index)
	}
	if err := receiver.Ack(sequence); err != nil {
		t.Fatal(err)
	}
}

func requireLogTailDrainClosed(t *testing.T, receiver *hub.Receiver) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := receiver.Next(ctx); !errors.Is(err, hub.ErrClosed) {
		t.Fatalf("next after final ACK = %v, want hub.ErrClosed", err)
	}
}

func TestLogTailJSONSinkLateBindDrainsTerminalEvents(t *testing.T) {
	h := hub.New(hub.Options{})
	t.Cleanup(func() { _ = h.Close() })
	sink := newLogTailDrainSink(t, h, "log-tail-late-bind")
	sendLogTailDrainEvents(t, sink)
	if err := sink.CloseGracefully(); err != nil {
		t.Fatal(err)
	}

	receiver, err := h.Bind("log-tail-late-bind")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	for index := range 3 {
		assertLogTailDrainEvent(t, receiver, index)
	}
	requireLogTailDrainClosed(t, receiver)
}

func TestLogTailJSONSinkInflightACKDrainsTerminalEvents(t *testing.T) {
	h := hub.New(hub.Options{})
	t.Cleanup(func() { _ = h.Close() })
	sink := newLogTailDrainSink(t, h, "log-tail-hold-ack")
	receiver, err := h.Bind("log-tail-hold-ack")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	sendLogTailDrainEvents(t, sink)
	first, firstSequence := nextLogTailDrainEvent(t, receiver)
	if first.Kind != logtail.EventLine || first.Text != "last-line" {
		t.Fatalf("in-flight event = %+v", first)
	}
	if err := sink.CloseGracefully(); err != nil {
		t.Fatal(err)
	}
	if err := receiver.Ack(firstSequence); err != nil {
		t.Fatal(err)
	}
	for index := 1; index < 3; index++ {
		assertLogTailDrainEvent(t, receiver, index)
	}
	requireLogTailDrainClosed(t, receiver)
}
