package takeover

import (
	"context"
	"reflect"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestTakeoverSequencesTextReasoningAndTerminal(t *testing.T) {
	reasoning := schema.AssistantMessage("", nil)
	reasoning.ReasoningContent = "same"
	chat := &fakeModel{stream: func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		return schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("same", nil),
			reasoning,
			schema.AssistantMessage("same", []schema.ToolCall{doneCall("finished")}),
		}), nil
	}}
	h := newHarness(t, chat)
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
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
			textSequences = append(textSequences, event.Seq)
		case "reasoning":
			reasoningSequences = append(reasoningSequences, event.Seq)
		}
	}
	if !reflect.DeepEqual(textSequences, []uint64{2, 4}) || !reflect.DeepEqual(reasoningSequences, []uint64{3}) {
		t.Fatalf("text sequences = %v, reasoning sequences = %v, events = %+v", textSequences, reasoningSequences, events)
	}
	if events[len(events)-1].Type != "done" || events[len(events)-1].Seq != uint64(len(events)) {
		t.Fatalf("terminal event = %+v", events[len(events)-1])
	}
}
