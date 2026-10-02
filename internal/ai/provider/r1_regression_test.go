package provider

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const textPartsBlock = `{"model":"served-parts","choices":[{"message":{"content":[{"type":"text","text":"Hel"},{"type":"text","text":"lo"}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`

func TestBlockTextPartContentRegression(t *testing.T) {
	t.Run("legacy block Chat", func(t *testing.T) {
		provider := &fakeProvider{steps: []fakeStep{{status: http.StatusOK, body: textPartsBlock}}}
		client := newFakeClient(t, provider, Config{Model: "m"}, 0, nil, nil)
		var items []StreamItem
		completion, err := client.Chat(context.Background(), ChatRequest{}, func(item StreamItem) {
			items = append(items, item)
		})
		if err != nil {
			t.Fatal(err)
		}
		if completion.Content != "Hello" || completion.Model != "served-parts" {
			t.Fatalf("completion = %+v", completion)
		}
		if !reflect.DeepEqual(items, []StreamItem{{Kind: StreamDelta, Text: "Hello"}}) {
			t.Fatalf("block items = %#v", items)
		}
	})
	t.Run("native Generate", func(t *testing.T) {
		provider := &fakeProvider{steps: []fakeStep{{status: http.StatusOK, body: textPartsBlock}}}
		client := newFakeClient(t, provider, Config{Model: "m"}, 0, nil, nil)
		chatModel, err := client.ChatModel(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		message, err := chatModel.Generate(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if message.Content != "Hello" || MetadataFromMessage(message).Model != "served-parts" {
			t.Fatalf("native message = %+v", message)
		}
	})
}

type countingReadCloser struct {
	reader io.Reader
	read   atomic.Int64
}

func (b *countingReadCloser) Read(target []byte) (int, error) {
	read, err := b.reader.Read(target)
	b.read.Add(int64(read))
	return read, err
}

func (b *countingReadCloser) Close() error {
	return nil
}

func TestOversizedErrorBodyBoundAndRetryBudget(t *testing.T) {
	oversized := `{"error":{"message":"` + strings.Repeat("x", 128<<10) + `"}}`
	bodies := make([]*countingReadCloser, 0, 4)
	steps := make([]fakeStep, 0, 4)
	for range 4 {
		body := &countingReadCloser{reader: strings.NewReader(oversized)}
		bodies = append(bodies, body)
		steps = append(steps, fakeStep{status: http.StatusInternalServerError, bodyOverride: body})
	}
	provider := &fakeProvider{steps: steps}
	client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup"}, 1, nil, nil)
	_, err := client.ChatBlock(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("oversized provider errors succeeded")
	}
	if len(err.Error()) > 500 {
		t.Fatalf("rendered error has %d bytes, want <= 500", len(err.Error()))
	}
	if ClassifyError(err) != ErrorClassServer {
		t.Fatalf("error class = %s, error = %v", ClassifyError(err), err)
	}
	if status, ok := statusCodeForError(err); !ok || status != http.StatusInternalServerError {
		t.Fatalf("status = %d, ok=%v, error=%v", status, ok, err)
	}
	if got := requestModels(provider.Requests()); !reflect.DeepEqual(got, []string{"primary", "primary", "backup", "backup"}) {
		t.Fatalf("retry/fallback requests = %v", got)
	}
	for index, body := range bodies {
		if got := body.read.Load(); got != maxErrorBody {
			t.Errorf("response %d read %d bytes, want bounded %d", index, got, maxErrorBody)
		}
	}
}
