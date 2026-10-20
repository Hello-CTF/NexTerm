package production

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/logtail"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

func TestLogTailDrainTimeoutCollectsChannelAndOwnerWithoutReceiver(t *testing.T) {
	const (
		channelID = "log-tail-no-reconnect"
		ownerID   = "owner-a"
	)
	h := hub.New(hub.Options{DrainTimeout: 250 * time.Millisecond})
	t.Cleanup(func() { _ = h.Close() })
	manager := session.NewManager(session.Config{Hub: h})
	t.Cleanup(func() { _ = manager.Close() })
	factory := &channelOwnerStreamFactory{factory: hub.StreamFactory{Hub: h}, sessions: manager}
	ctx := ipc.WithUserID(context.Background(), ownerID)
	raw, err := factory.OpenJSON(ctx, ipc.ChannelRef{ID: channelID})
	if err != nil {
		t.Fatal(err)
	}
	sink := &logTailJSONSink{stream: ipc.NewTypedJSONStream[logtail.Event](raw), raw: raw}
	service := logtail.NewWithDrainTimeout(logtail.Config{Resolver: logtail.ResolverFunc(func(context.Context, string) (logtail.Target, error) {
		return logtail.Target{}, errors.New("start failed")
	})}, 250*time.Millisecond)
	_, err = service.Start(ctx, ownerID, logtail.Request{
		RunID:   "no-reconnect",
		Sources: []logtail.Source{{SourceID: "source", SessionID: "session", Kind: logtail.SourceFile, Value: "/var/log/app.log"}},
	}, sink)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	for h.Stats().QueuedFrames < 4 {
		select {
		case <-deadline:
			t.Fatalf("log tail did not queue its terminal events: %+v", h.Stats())
		case <-time.After(2 * time.Millisecond):
		}
	}
	if owner, _, err := manager.ChannelOwnerGeneration(channelID); err != nil || owner != ownerID {
		t.Fatalf("owner before drain timeout = %q, %v", owner, err)
	}
	receiver, err := h.Bind(channelID)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Close(); err != nil {
		t.Fatal(err)
	}

	for {
		incarnation, _ := h.ChannelCloser(channelID)
		_, _, ownerErr := manager.ChannelOwnerGeneration(channelID)
		if incarnation == 0 && errors.Is(ownerErr, session.ErrTabNotFound) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("channel/owner survived drain timeout: incarnation=%d ownerErr=%v stats=%+v", incarnation, ownerErr, h.Stats())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if stats := h.Stats(); stats.QueuedFrames != 0 || stats.PendingChannels != 0 {
		t.Fatalf("expired drain retained backlog: %+v", stats)
	}
}
