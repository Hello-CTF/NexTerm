package hub

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestStreamFactoryRoutesBinaryAndTypedJSONThroughSharedHub(t *testing.T) {
	h := New(Options{})
	factory := StreamFactory{Hub: h}
	binary, err := factory.OpenBinary(context.Background(), ipc.ChannelRef{ID: "terminal"})
	if err != nil {
		t.Fatal(err)
	}
	defer binary.Close()
	type aiEvent struct {
		Type string `json:"type"`
	}
	ai, err := ipc.OpenTypedJSONStream[aiEvent](context.Background(), factory, ipc.ChannelRef{ID: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	defer ai.Close()
	if err := binary.SendBinary(context.Background(), []byte{0, 0xff}); err != nil {
		t.Fatal(err)
	}
	if err := ai.Send(context.Background(), aiEvent{Type: "done"}); err != nil {
		t.Fatal(err)
	}
	terminalReceiver, err := h.Bind("terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer terminalReceiver.Close()
	aiReceiver, err := h.Bind("ai")
	if err != nil {
		t.Fatal(err)
	}
	defer aiReceiver.Close()
	terminalFrame := receiveFrame(t, terminalReceiver)
	if terminalFrame.Kind != FrameBinary || string(terminalFrame.Data) != string([]byte{0, 0xff}) {
		t.Fatalf("terminal frame = %+v", terminalFrame)
	}
	aiFrame := receiveFrame(t, aiReceiver)
	if aiFrame.Kind != FrameJSON || !json.Valid(aiFrame.Data) || string(aiFrame.Data) != `{"type":"done"}` {
		t.Fatalf("AI frame = %+v", aiFrame)
	}
}

func TestStreamFactoryRequiresHub(t *testing.T) {
	_, err := (StreamFactory{}).OpenBinary(context.Background(), ipc.ChannelRef{ID: "terminal"})
	if !errors.Is(err, ipc.ErrStreamsUnavailable) {
		t.Fatalf("nil hub error = %v", err)
	}
}
