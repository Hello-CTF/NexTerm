package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type recordedJSONStream struct {
	frames []json.RawMessage
	closed int
}

func (s *recordedJSONStream) SendJSON(_ context.Context, frame json.RawMessage) error {
	s.frames = append(s.frames, append(json.RawMessage(nil), frame...))
	return nil
}

func (s *recordedJSONStream) Close() error {
	s.closed++
	return nil
}

func TestTypedJSONStreamMarshalsDomainEvents(t *testing.T) {
	type aiEvent struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	}

	raw := &recordedJSONStream{}
	stream := NewTypedJSONStream[aiEvent](raw)
	if err := stream.Send(context.Background(), aiEvent{Type: "delta", Text: "你好"}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(context.Background(), aiEvent{Type: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw.frames[0]), `{"type":"delta","text":"你好"}`; got != want {
		t.Fatalf("first frame = %s, want %s", got, want)
	}
	if got, want := string(raw.frames[1]), `{"type":"done"}`; got != want {
		t.Fatalf("second frame = %s, want %s", got, want)
	}
	if raw.closed != 1 {
		t.Fatalf("close count = %d", raw.closed)
	}
}

func TestStreamFactoryReportsMissingAdapters(t *testing.T) {
	_, err := OpenTypedJSONStream[any](context.Background(), nil, ChannelRef{ID: "job"})
	if !errors.Is(err, ErrStreamsUnavailable) {
		t.Fatalf("nil factory error = %v", err)
	}
	_, err = (StreamFactoryFuncs{}).OpenBinary(context.Background(), ChannelRef{ID: "pty"})
	if !errors.Is(err, ErrStreamsUnavailable) {
		t.Fatalf("missing binary adapter error = %v", err)
	}
}

func TestChannelRefAcceptsStringShapes(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: `"abc"`, want: "abc"},
		{input: `""`, want: ""},
		{input: `null`, want: ""},
	} {
		var channel ChannelRef
		if err := json.Unmarshal([]byte(test.input), &channel); err != nil {
			t.Fatalf("Unmarshal(%s): %v", test.input, err)
		}
		if channel.ID != test.want {
			t.Fatalf("Unmarshal(%s) = %q, want %q", test.input, channel.ID, test.want)
		}
	}
	for _, input := range []string{`42`, `{"id":"abc"}`, `{"channelId":42}`, `true`} {
		var channel ChannelRef
		if err := json.Unmarshal([]byte(input), &channel); err == nil {
			t.Fatalf("Unmarshal(%s) succeeded with ID %q, want error", input, channel.ID)
		}
	}
}
