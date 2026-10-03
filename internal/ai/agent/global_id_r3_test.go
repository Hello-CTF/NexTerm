package agent

import (
	"context"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestR3AgentDuplicateJobIDRejectsBeforeFactory(t *testing.T) {
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	runner, _ := testRunner(t, chat, tools.Dependencies{}, 0)
	runner.config.NewID = func() string { return "duplicate" }
	response := startTestJob(t, runner, &SliceStream{}, "go")
	factoryCalls := 0
	_, err := runner.Start(context.Background(), ChatArgs{Message: "second"}, func(context.Context, string, string) (Stream, error) {
		factoryCalls++
		return &SliceStream{}, nil
	})
	if err == nil {
		t.Fatal("duplicate job ID accepted")
	}
	if factoryCalls != 0 {
		t.Fatalf("duplicate job ID opened factory %d times", factoryCalls)
	}
	if err := runner.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
}
