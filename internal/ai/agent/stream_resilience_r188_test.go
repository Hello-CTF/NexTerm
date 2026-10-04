package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/hub"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestResilientStreamKicksFullQueueWithoutFreezingRun(t *testing.T) {
	storage := restartStore(t)
	bus := hub.New(hub.Options{QueueFrames: 1})
	t.Cleanup(func() { _ = bus.Close() })
	chat := &fakeModel{steps: []fakeStep{{chunks: []*schema.Message{
		schema.AssistantMessage("第一段", nil),
		schema.AssistantMessage("第二段", nil),
		schema.AssistantMessage("第三段", nil),
	}}}}
	runner := durableRunner(t, storage, chat, tools.Dependencies{}, nil)
	response, err := runner.Start(context.Background(), ChatArgs{Message: "go", ChannelID: "channel", Scope: tools.Scope{SessionID: "session"}}, ResilientIPCStreamFactory(hub.StreamFactory{Hub: bus}))
	if err != nil {
		t.Fatal(err)
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
	events := runEventsOf(t, storage, response.JobID)
	requireContiguousSeq(t, events)
	if events[len(events)-1].Type != "done" {
		t.Fatalf("terminal journal event = %+v", events[len(events)-1])
	}
	deltas := 0
	for _, event := range events {
		if event.Type == "delta" {
			deltas++
		}
	}
	if deltas != 3 {
		t.Fatalf("journaled deltas = %d, want 3: %+v", deltas, events)
	}
	if stats := bus.Stats(); stats.LiveChannels != 0 || stats.PendingChannels > 1 {
		t.Fatalf("kicked channel remains: %+v", stats)
	}
}

func TestResilientStreamKickKeepsJournalReplayable(t *testing.T) {
	storage := restartStore(t)
	bus := hub.New(hub.Options{QueueFrames: 1})
	t.Cleanup(func() { _ = bus.Close() })
	chat := &fakeModel{steps: []fakeStep{{chunks: []*schema.Message{
		schema.AssistantMessage("甲", nil),
		schema.AssistantMessage("乙", nil),
	}}}}
	runner := durableRunner(t, storage, chat, tools.Dependencies{}, nil)
	response, err := runner.Start(context.Background(), ChatArgs{Message: "go", ChannelID: "channel", Scope: tools.Scope{SessionID: "session"}}, ResilientIPCStreamFactory(hub.StreamFactory{Hub: bus}))
	if err != nil {
		t.Fatal(err)
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
	events := runEventsOf(t, storage, response.JobID)
	requireContiguousSeq(t, events)
	replayed, err := runner.RunEvents(context.Background(), response.JobID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != len(events) {
		t.Fatalf("replay has %d events, journal has %d", len(replayed), len(events))
	}
	after, err := runner.RunEvents(context.Background(), response.JobID, events[len(events)-1].Seq)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("afterSeq replay returned %d events, want 0: %+v", len(after), after)
	}
}

func TestResilientStreamRedialsClosedChannel(t *testing.T) {
	bus := hub.New(hub.Options{})
	t.Cleanup(func() { _ = bus.Close() })
	stream, err := ResilientIPCStreamFactory(hub.StreamFactory{Hub: bus})(context.Background(), "channel", "job")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(context.Background(), Event{Type: "delta", Text: "before"}); err != nil {
		t.Fatal(err)
	}
	if err := bus.CloseChannel("channel"); err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(context.Background(), Event{Type: "delta", Text: "resumed"}); err != nil {
		t.Fatalf("send after channel close = %v, want redial", err)
	}
	receiver, err := bus.Bind("channel")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	event := nextHubEvent(t, receiver)
	if event["type"] != "delta" || event["text"] != "resumed" {
		t.Fatalf("redialed event = %v", event)
	}
}

func TestResilientStreamHubClosedReturnsError(t *testing.T) {
	bus := hub.New(hub.Options{})
	stream, err := ResilientIPCStreamFactory(hub.StreamFactory{Hub: bus})(context.Background(), "channel", "job")
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(context.Background(), Event{Type: "delta", Text: "x"}); !errors.Is(err, hub.ErrHubClosed) {
		t.Fatalf("send on closed hub = %v, want ErrHubClosed", err)
	}
}

func TestResilientStreamFallsBackToBlockingSend(t *testing.T) {
	wire := &recordedSequenceWire{}
	factory := ipc.StreamFactoryFuncs{JSON: func(context.Context, ipc.ChannelRef) (ipc.JSONStream, error) {
		return wire, nil
	}}
	stream, err := ResilientIPCStreamFactory(factory)(context.Background(), "channel", "job")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := stream.Send(context.Background(), Event{Type: "delta", Text: "same"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(wire.frames) != 2 {
		t.Fatalf("wire frames = %d, want 2", len(wire.frames))
	}
}

func TestUserCancelJournalsCanceledTerminal(t *testing.T) {
	storage := restartStore(t)
	bus := hub.New(hub.Options{})
	t.Cleanup(func() { _ = bus.Close() })
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	runner := durableRunner(t, storage, chat, tools.Dependencies{}, nil)
	response, err := runner.Start(context.Background(), ChatArgs{Message: "go", ChannelID: "channel", Scope: tools.Scope{SessionID: "session"}}, ResilientIPCStreamFactory(hub.StreamFactory{Hub: bus}))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusCanceled)
	events := runEventsOf(t, storage, response.JobID)
	requireContiguousSeq(t, events)
	last := events[len(events)-1]
	if last.Type != "canceled" {
		t.Fatalf("terminal journal event = %+v, want canceled", last)
	}
	if strings.Contains(last.PayloadJSON, "retryable") {
		t.Fatalf("canceled event must not carry retryable: %s", last.PayloadJSON)
	}
}

func TestCancellationClassification(t *testing.T) {
	if !isCancellation(context.Canceled) || !isCancellation(context.DeadlineExceeded) || !isCancellation(&adk.CancelError{}) {
		t.Fatal("cancellation shapes not classified as user cancel")
	}
	if isCancellation(errors.New("boom")) || isCancellation(nil) {
		t.Fatal("non-cancellation misclassified")
	}
}

func TestRestartCanceledRestoredRunJournalsCanceledTerminal(t *testing.T) {
	storage := restartStore(t)
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	_ = waitEvent(t, stream, "confirmRequired")

	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), deps, nil)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusCanceled)
	deadline := time.Now().Add(5 * time.Second)
	for {
		events := runEventsOf(t, storage, response.JobID)
		requireContiguousSeq(t, events)
		if last := events[len(events)-1]; last.Type == "canceled" {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("restored cancel terminal journal event = %+v", last)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
