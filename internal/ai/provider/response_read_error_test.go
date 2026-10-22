package provider

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func sentinelReadFailureSteps(sentinel error, count int) ([]fakeStep, []*countingReadCloser) {
	steps := make([]fakeStep, 0, count)
	bodies := make([]*countingReadCloser, 0, count)
	for range count {
		body := &countingReadCloser{reader: strings.NewReader(strings.Repeat("x", maxErrorBody)), readErr: sentinel}
		bodies = append(bodies, body)
		steps = append(steps, fakeStep{status: http.StatusInternalServerError, bodyOverride: body})
	}
	return steps, bodies
}

func assertStatusAndSentinel(t *testing.T, err, sentinel error, requests []recordedRequest) {
	t.Helper()
	if err == nil {
		t.Fatal("injected provider failures succeeded")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("sentinel was lost: %v", err)
	}
	if len(err.Error()) > 500 {
		t.Fatalf("rendered error has %d bytes, want <= 500", len(err.Error()))
	}
	if ClassifyError(err) != ErrorClassServer {
		t.Fatalf("error class = %s, error=%v", ClassifyError(err), err)
	}
	if status, ok := statusCodeForError(err); !ok || status != http.StatusInternalServerError {
		t.Fatalf("status = %d, ok=%v, error=%v", status, ok, err)
	}
	if got := requestModels(requests); !reflect.DeepEqual(got, []string{"primary", "primary", "backup", "backup"}) {
		t.Fatalf("retry/fallback requests = %v", got)
	}
}

func TestBlockReadFailurePreservesStatusAndSentinel(t *testing.T) {
	sentinel := errors.New("block-read-sentinel-" + strings.Repeat("s", 2000))
	steps, bodies := sentinelReadFailureSteps(sentinel, 4)
	provider := &fakeProvider{steps: steps}
	client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup"}, 1, nil, nil)
	_, err := client.ChatBlock(context.Background(), ChatRequest{})
	assertStatusAndSentinel(t, err, sentinel, provider.Requests())
	assertClosedBoundedBodies(t, bodies)
}

func TestNativeStreamReadFailurePreservesStatusAndSentinel(t *testing.T) {
	sentinel := errors.New("stream-read-sentinel-" + strings.Repeat("s", 2000))
	steps, bodies := sentinelReadFailureSteps(sentinel, 4)
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
	assertStatusAndSentinel(t, streamErr, sentinel, provider.Requests())
	assertClosedBoundedBodies(t, bodies)
}

func TestResponseAndErrorPreservesStatusAndSentinel(t *testing.T) {
	sentinel := errors.New("roundtrip-response-sentinel-" + strings.Repeat("s", 2000))
	steps := make([]fakeStep, 0, 4)
	bodies := make([]*countingReadCloser, 0, 4)
	for range 4 {
		body := &countingReadCloser{reader: strings.NewReader("")}
		bodies = append(bodies, body)
		steps = append(steps, fakeStep{status: http.StatusInternalServerError, bodyOverride: body, err: sentinel})
	}
	provider := &fakeProvider{steps: steps}
	client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup"}, 1, nil, nil)
	_, err := client.ChatBlock(context.Background(), ChatRequest{})
	assertStatusAndSentinel(t, err, sentinel, provider.Requests())
	for index, body := range bodies {
		if got := body.read.Load(); got > maxErrorBody {
			t.Errorf("response %d read %d bytes, want <= %d", index, got, maxErrorBody)
		}
		if got := body.closed.Load(); got != 1 {
			t.Errorf("response %d was closed %d times, want exactly once", index, got)
		}
	}
}
