package session

import (
	"context"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func TestInitialConnectCancellationUnblocksDial(t *testing.T) {
	for _, action := range []string{"disconnect", "close"} {
		t.Run(action, func(t *testing.T) {
			connector := newFakeConnector()
			started := make(chan struct{})
			connector.connect = func(ctx context.Context, _ Asset, _ uint64, _ int) (base.Transport, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
			t.Cleanup(func() { _ = manager.Close() })
			result := make(chan error, 1)
			go func() {
				_, err := manager.Connect(context.Background(), Asset{ID: "blocked-dial", Kind: KindSSH})
				result <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("connector did not start")
			}
			infos := manager.ListSessions()
			if len(infos) != 1 {
				t.Fatalf("connecting sessions = %+v", infos)
			}
			if action == "disconnect" {
				if err := manager.Disconnect(infos[0].ID); err != nil {
					t.Fatal(err)
				}
			} else if err := manager.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("canceled initial connect succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("session cancellation did not interrupt blocked dial")
			}
		})
	}
}
