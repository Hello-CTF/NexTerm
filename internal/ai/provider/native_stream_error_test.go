package provider

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func oversizedErrorSteps(payload string, count int) ([]fakeStep, []*countingReadCloser) {
	steps := make([]fakeStep, 0, count)
	bodies := make([]*countingReadCloser, 0, count)
	for range count {
		body := &countingReadCloser{reader: strings.NewReader(payload)}
		bodies = append(bodies, body)
		steps = append(steps, fakeStep{status: http.StatusInternalServerError, bodyOverride: body})
	}
	return steps, bodies
}

func assertClosedBoundedBodies(t *testing.T, bodies []*countingReadCloser) {
	t.Helper()
	for index, body := range bodies {
		if got := body.read.Load(); got != maxErrorBody {
			t.Errorf("response %d read %d bytes, want %d", index, got, maxErrorBody)
		}
		if got := body.closed.Load(); got != 1 {
			t.Errorf("response %d was closed %d times, want exactly once", index, got)
		}
	}
}

func TestNativeStreamOversizedErrorsCloseEveryAttempt(t *testing.T) {
	oversized := `{"error":{"message":"` + strings.Repeat("x", 128<<10) + `"}}`
	steps, bodies := oversizedErrorSteps(oversized, 4)
	provider := &fakeProvider{steps: steps}
	client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup"}, 1, nil, nil)
	chatModel, err := client.ChatModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reader, err := chatModel.Stream(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var streamErr error
	for {
		_, recvErr := reader.Recv()
		if recvErr != nil {
			streamErr = recvErr
			break
		}
	}
	if streamErr == nil {
		t.Fatal("oversized streaming errors succeeded")
	}
	if len(streamErr.Error()) > 500 {
		t.Fatalf("rendered stream error has %d bytes, want <= 500", len(streamErr.Error()))
	}
	if ClassifyError(streamErr) != ErrorClassServer {
		t.Fatalf("stream error class = %s, error=%v", ClassifyError(streamErr), streamErr)
	}
	if status, ok := statusCodeForError(streamErr); !ok || status != http.StatusInternalServerError {
		t.Fatalf("stream status = %d, ok=%v, error=%v", status, ok, streamErr)
	}
	if got := requestModels(provider.Requests()); !reflect.DeepEqual(got, []string{"primary", "primary", "backup", "backup"}) {
		t.Fatalf("streaming retry/fallback requests = %v", got)
	}
	assertClosedBoundedBodies(t, bodies)
}

func TestNativeStreamOversizedErrorClosesBeforeCancellation(t *testing.T) {
	oversized := `{"error":{"message":"` + strings.Repeat("x", 128<<10) + `"}}`
	steps, bodies := oversizedErrorSteps(oversized, 1)
	provider := &fakeProvider{steps: steps}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup"}, 3,
		func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		}, nil,
	)
	chatModel, err := client.ChatModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reader, err := chatModel.Stream(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	_, streamErr := reader.Recv()
	if !errors.Is(streamErr, context.Canceled) {
		t.Fatalf("stream cancellation error = %v", streamErr)
	}
	if got := requestModels(provider.Requests()); !reflect.DeepEqual(got, []string{"primary"}) {
		t.Fatalf("requests after cancellation = %v", got)
	}
	assertClosedBoundedBodies(t, bodies)
}
