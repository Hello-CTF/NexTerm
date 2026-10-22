package hub

import (
	"context"
	"testing"
	"time"
)

func TestProducerGracefulCloseTimeoutCollectsBacklog(t *testing.T) {
	h := New(Options{DrainTimeout: 40 * time.Millisecond})
	t.Cleanup(func() { _ = h.Close() })
	producer, err := h.Producer("expired-drain")
	if err != nil {
		t.Fatal(err)
	}
	if err := producer.SendJSON(context.Background(), []byte(`{"type":"done"}`)); err != nil {
		t.Fatal(err)
	}
	if err := producer.CloseGracefully(); err != nil {
		t.Fatal(err)
	}
	requireChannelRetained(t, h, "expired-drain")

	deadline := time.After(2 * time.Second)
	for {
		h.mu.Lock()
		_, retained := h.channels["expired-drain"]
		h.mu.Unlock()
		if !retained {
			return
		}
		select {
		case <-deadline:
			t.Fatal("draining channel was not collected after its timeout")
		case <-time.After(5 * time.Millisecond):
		}
	}
}
