package production

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type recordingChannelStream struct {
	binary [][]byte
	json   []json.RawMessage
}

func (s *recordingChannelStream) SendBinary(_ context.Context, data []byte) error {
	s.binary = append(s.binary, append([]byte(nil), data...))
	return nil
}

func (s *recordingChannelStream) SendJSON(_ context.Context, data json.RawMessage) error {
	s.json = append(s.json, append(json.RawMessage(nil), data...))
	return nil
}

func (s *recordingChannelStream) Close() error { return nil }

type binaryOnlyStream struct{ sent int }

func (s *binaryOnlyStream) SendBinary(context.Context, []byte) error {
	s.sent++
	return nil
}

func (s *binaryOnlyStream) Close() error { return nil }

// A resumed exec's replay announcement reaches the channel as an in-band
// JSON frame, also through the channel-owner wrapper production opens.
func TestDockerExecSinkAnnouncesReplayInBand(t *testing.T) {
	for name, wrap := range map[string]func(ipc.BinaryStream) ipc.BinaryStream{
		"direct":        func(stream ipc.BinaryStream) ipc.BinaryStream { return stream },
		"channel owner": func(stream ipc.BinaryStream) ipc.BinaryStream { return &channelOwnerBinaryStream{BinaryStream: stream} },
	} {
		t.Run(name, func(t *testing.T) {
			inner := &recordingChannelStream{}
			sink := dockerExecSink{stream: wrap(inner)}
			if err := sink.AnnounceReplay(t.Context(), 7); err != nil {
				t.Fatal(err)
			}
			if err := sink.Send(t.Context(), []byte("backlog")); err != nil {
				t.Fatal(err)
			}
			if len(inner.json) != 1 {
				t.Fatalf("json frames = %q", inner.json)
			}
			var frame struct {
				Type  string `json:"type"`
				Bytes int    `json:"bytes"`
			}
			if err := json.Unmarshal(inner.json[0], &frame); err != nil || frame.Type != streamReplayType || frame.Bytes != 7 {
				t.Fatalf("announcement = %s (err %v)", inner.json[0], err)
			}
			if len(inner.binary) != 1 || string(inner.binary[0]) != "backlog" {
				t.Fatalf("binary frames = %q", inner.binary)
			}
		})
	}

	// Without an in-band control path the backlog is delivered unmarked.
	plain := &binaryOnlyStream{}
	if err := (dockerExecSink{stream: &channelOwnerBinaryStream{BinaryStream: plain}}).AnnounceReplay(t.Context(), 3); err != nil {
		t.Fatalf("announce without JSON support = %v", err)
	}
	if plain.sent != 0 {
		t.Fatalf("announcement leaked into the byte stream")
	}
}
