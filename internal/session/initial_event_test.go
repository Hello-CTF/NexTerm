package session

import (
	"context"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type initialStatusEmitter struct {
	started chan struct{}
	release chan struct{}
	events  chan StatusEvent
}

func (e *initialStatusEmitter) EmitSessionEvent(_ context.Context, event Event) error {
	if event.Topic != TopicSessionStatus {
		return nil
	}
	payload := event.Payload.(StatusEvent)
	if payload.Status == StatusConnecting {
		close(e.started)
		<-e.release
	}
	e.events <- payload
	return nil
}

func TestInitialConnectDisconnectVersionsMatchAuthority(t *testing.T) {
	emitter := &initialStatusEmitter{started: make(chan struct{}), release: make(chan struct{}), events: make(chan StatusEvent, 2)}
	connector := newFakeConnector()
	connector.connect = func(ctx context.Context, _ Asset, _ uint64, _ int) (base.Transport, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: emitter})
	t.Cleanup(func() { _ = manager.Close() })
	result := make(chan error, 1)
	go func() {
		_, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
		result <- err
	}()
	<-emitter.started
	infos := manager.ListSessions()
	if len(infos) != 1 {
		t.Fatalf("connecting registry = %+v", infos)
	}
	if err := manager.Disconnect(infos[0].ID); err != nil {
		t.Fatal(err)
	}
	close(emitter.release)
	disconnected := <-emitter.events
	connecting := <-emitter.events
	if disconnected.Status != StatusDisconnected || disconnected.Version != 2 {
		t.Fatalf("disconnect snapshot = %+v, want version 2", disconnected)
	}
	if connecting.Status != StatusConnecting || connecting.Version != 1 {
		t.Fatalf("initial snapshot = %+v, want version 1", connecting)
	}
	latest := connecting
	if disconnected.Version > latest.Version {
		latest = disconnected
	}
	current, err := manager.Session(infos[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if authority := current.Info().Status; latest.Status != authority {
		t.Fatalf("latest event %s does not match authority %s", latest.Status, authority)
	}
	if err := <-result; err == nil {
		t.Fatal("canceled initial connect succeeded")
	}
}
