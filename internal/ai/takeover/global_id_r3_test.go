package takeover

import (
	"context"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestR3TakeoverGlobalDuplicateJobIDRejectsBeforeFactory(t *testing.T) {
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	h := newHarness(t, chat)
	h.manager.deps.NewID = func() string { return "duplicate" }
	firstToken, err := h.manager.Enter(context.Background(), "tab")
	if err != nil {
		t.Fatal(err)
	}
	first := &agent.SliceStream{}
	response, err := h.manager.Run(context.Background(), RunArgs{TabID: "tab", Token: firstToken, Instruction: "go"}, agent.StaticStream(first))
	if err != nil {
		t.Fatal(err)
	}
	secondToken, err := h.manager.Enter(context.Background(), "tab")
	if err != nil {
		t.Fatal(err)
	}
	factoryCalls := 0
	_, err = h.manager.Run(context.Background(), RunArgs{TabID: "tab", Token: secondToken, Instruction: "second"}, func(context.Context, string, string) (agent.Stream, error) {
		factoryCalls++
		return &agent.SliceStream{}, nil
	})
	if err == nil {
		t.Fatal("global duplicate job ID accepted")
	}
	if factoryCalls != 0 {
		t.Fatalf("global duplicate job ID opened factory %d times", factoryCalls)
	}
	if err := h.manager.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	_ = waitClosed(t, first)
}
