package takeover

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type duplicateRunResult struct {
	response RunResponse
	stream   *agent.SliceStream
	err      error
}

func TestR1ConcurrentDuplicateRunHasSingleWinner(t *testing.T) {
	started := make(chan struct{})
	var modelOnce sync.Once
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		modelOnce.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	h := newHarness(t, chat)
	token, err := h.manager.Enter(context.Background(), "tab")
	if err != nil {
		t.Fatal(err)
	}
	begin := make(chan struct{})
	results := make(chan duplicateRunResult, 2)
	for _, instruction := range []string{"first", "second"} {
		go func(instruction string) {
			stream := &agent.SliceStream{}
			<-begin
			response, err := h.manager.Run(context.Background(), RunArgs{TabID: "tab", Token: token, Instruction: instruction}, agent.StaticStream(stream))
			results <- duplicateRunResult{response: response, stream: stream, err: err}
		}(instruction)
	}
	close(begin)
	var winner *duplicateRunResult
	for range 2 {
		result := <-results
		if result.err == nil {
			if winner != nil {
				t.Fatalf("two active runs accepted: %+v", result)
			}
			winner = &result
			continue
		}
		if !errors.Is(result.err, ErrOwnershipActive) {
			t.Fatalf("duplicate run error = %v", result.err)
		}
	}
	if winner == nil {
		t.Fatal("no run won the ownership reservation")
	}
	<-started
	if err := h.manager.Cancel(winner.response.JobID); err != nil {
		t.Fatal(err)
	}
	_ = waitClosed(t, winner.stream)
}
