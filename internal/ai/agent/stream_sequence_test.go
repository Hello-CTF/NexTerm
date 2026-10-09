package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/cloudwego/eino/schema"
)

func TestRunSequencesIdenticalNewTextAndReasoning(t *testing.T) {
	delta := schema.AssistantMessage("same", nil)
	reasoning := schema.AssistantMessage("", nil)
	reasoning.ReasoningContent = "same"
	chat := &fakeModel{steps: []fakeStep{{chunks: []*schema.Message{delta, reasoning, delta, reasoning}}}}
	runner, _ := testRunner(t, chat, tools.Dependencies{}, 0)
	stream := &SliceStream{}
	startTestJob(t, runner, stream, "go")
	events := waitClosed(t, stream)
	for index, event := range events {
		if event.Seq != uint64(index+1) {
			t.Fatalf("event %d sequence = %d, events = %+v", index, event.Seq, events)
		}
	}
	var textSequences, reasoningSequences []uint64
	for _, event := range events {
		switch event.Type {
		case "delta":
			if event.Text != "same" {
				t.Fatalf("delta text = %q", event.Text)
			}
			textSequences = append(textSequences, event.Seq)
		case "reasoning":
			if event.Text != "same" {
				t.Fatalf("reasoning text = %q", event.Text)
			}
			reasoningSequences = append(reasoningSequences, event.Seq)
		}
	}
	if !reflect.DeepEqual(textSequences, []uint64{2, 4}) || !reflect.DeepEqual(reasoningSequences, []uint64{3, 5}) {
		t.Fatalf("text sequences = %v, reasoning sequences = %v, events = %+v", textSequences, reasoningSequences, events)
	}
	if events[len(events)-1].Type != "done" || events[len(events)-1].Seq != uint64(len(events)) {
		t.Fatalf("terminal event = %+v", events[len(events)-1])
	}
}

type recordedSequenceWire struct {
	frames []json.RawMessage
}

func (s *recordedSequenceWire) SendJSON(_ context.Context, frame json.RawMessage) error {
	s.frames = append(s.frames, append(json.RawMessage(nil), frame...))
	return nil
}

func (s *recordedSequenceWire) Close() error { return nil }

func TestIPCSequenceIdenticalNewAndReplayStable(t *testing.T) {
	wire := &recordedSequenceWire{}
	factory := ipc.StreamFactoryFuncs{JSON: func(context.Context, ipc.ChannelRef) (ipc.JSONStream, error) {
		return wire, nil
	}}
	stream, err := IPCStreamFactory(factory)(context.Background(), "channel", "job")
	if err != nil {
		t.Fatal(err)
	}
	stream = WithEventSequence(stream)
	for range 2 {
		if err := stream.Send(context.Background(), Event{Type: "delta", Text: "same"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(wire.frames) != 2 || bytes.Equal(wire.frames[0], wire.frames[1]) {
		t.Fatalf("identical new frames did not get distinct wire identities: %q", wire.frames)
	}
	if err := wire.SendJSON(context.Background(), wire.frames[1]); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wire.frames[1], wire.frames[2]) {
		t.Fatalf("replay changed the original frame: live %s replay %s", wire.frames[1], wire.frames[2])
	}
	var first, second, replayed map[string]any
	for index, target := range []*map[string]any{&first, &second, &replayed} {
		if err := json.Unmarshal(wire.frames[index], target); err != nil {
			t.Fatal(err)
		}
	}
	if first["seq"] != float64(1) || second["seq"] != float64(2) || replayed["seq"] != second["seq"] {
		t.Fatalf("first = %v, second = %v, replayed = %v", first, second, replayed)
	}
}

func TestHubReconnectKeepsReplaySequence(t *testing.T) {
	bus := hub.New(hub.Options{})
	t.Cleanup(func() { _ = bus.Close() })
	stream, err := IPCStreamFactory(hub.StreamFactory{Hub: bus})(context.Background(), "channel", "job")
	if err != nil {
		t.Fatal(err)
	}
	stream = WithEventSequence(stream)
	if err := stream.Send(context.Background(), Event{Type: "delta", Text: "same"}); err != nil {
		t.Fatal(err)
	}
	firstReceiver, err := bus.Bind("channel")
	if err != nil {
		t.Fatal(err)
	}
	firstFrame, err := firstReceiver.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := firstReceiver.Ack(firstFrame.Sequence); err != nil {
		t.Fatal(err)
	}
	if err := firstReceiver.Close(); err != nil {
		t.Fatal(err)
	}
	secondReceiver, err := bus.Bind("channel")
	if err != nil {
		t.Fatal(err)
	}
	defer secondReceiver.Close()
	if err := bus.SendJSON(context.Background(), "channel", firstFrame.Data); err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(context.Background(), Event{Type: "delta", Text: "same"}); err != nil {
		t.Fatal(err)
	}
	replayedFrame, err := secondReceiver.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := secondReceiver.Ack(replayedFrame.Sequence); err != nil {
		t.Fatal(err)
	}
	newFrame, err := secondReceiver.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(replayedFrame.Data, firstFrame.Data) {
		t.Fatalf("reconnect replay changed bytes: live %s replay %s", firstFrame.Data, replayedFrame.Data)
	}
	var replayed, newEvent map[string]any
	if err := json.Unmarshal(replayedFrame.Data, &replayed); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(newFrame.Data, &newEvent); err != nil {
		t.Fatal(err)
	}
	if replayed["seq"] != float64(1) || newEvent["seq"] != float64(2) || replayed["text"] != newEvent["text"] {
		t.Fatalf("replayed = %v, new = %v", replayed, newEvent)
	}
}

func TestEventSequenceIsPerRun(t *testing.T) {
	first := &SliceStream{}
	second := &SliceStream{}
	for _, stream := range []Stream{WithEventSequence(first), WithEventSequence(second)} {
		if err := stream.Send(context.Background(), Event{Type: "delta", Text: "same"}); err != nil {
			t.Fatal(err)
		}
	}
	for name, stream := range map[string]*SliceStream{"first": first, "second": second} {
		events, _ := stream.Snapshot()
		if len(events) != 1 || events[0].Seq != 1 {
			t.Fatalf("%s run events = %+v", name, events)
		}
	}
}
