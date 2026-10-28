//go:build unix

package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// Two managers (two devices) attached to one helper session see the
// helper's state, and a state seen on one device is seen on the other.
func TestAttentionHelperStateAndSeenAcrossManagers(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "supervisor")
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-session-attention-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	server, err := supervisor.NewServer(instance, filepath.Join(runDir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		_ = instance.Close()
	})
	newDevice := func() (*Manager, *attentionRecorder) {
		provider := supervisor.NewRemoteProvider(supervisor.NewClient(server.SocketPath(), stateDir))
		recorder := newAttentionRecorder()
		manager := NewManager(Config{
			Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Emitter: recorder, OpenAccess: true,
			DurableResolver: DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) {
				return provider, nil
			}),
		})
		t.Cleanup(func() { _ = manager.Close() })
		return manager, recorder
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	desktop, desktopEvents := newDevice()
	phone, phoneEvents := newDevice()

	tabID := ids.New()
	desktopSession, err := desktop.Connect(ctx, Asset{ID: "ssh-attention-a", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	// The bell rings well after the input that releases it, so it is not
	// taken as the reaction to that input.
	if _, err := desktop.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: desktopSession.ID, ClientID: "client-a", ChannelID: "attention-a1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh", "-c", "read go; sleep 1.5; printf 'needs you\\007'; sleep 30"}},
	}); err != nil {
		t.Fatal(err)
	}
	desktopEvents.wait(t, tabID, "helper state", func(event AttentionEvent) bool { return event.Source == AttentionSourceHelper })

	phoneSession, err := phone.Connect(ctx, Asset{ID: "ssh-attention-b", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	descriptors, err := phone.ListRemoteDurable(ctx, phoneSession.ID)
	if err != nil || len(descriptors) != 1 {
		t.Fatalf("remote descriptors = %+v, %v", descriptors, err)
	}
	if _, err := phone.RecoverRemoteDurable(ctx, descriptors[0].ID, OpenTabOptions{ClientID: "client-b", ChannelID: "attention-b1", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}

	if err := desktop.Write(ctx, tabID, "client-a", []byte("go\n")); err != nil {
		t.Fatal(err)
	}
	needsMe := attentionIs(base.SessionNeedsMe, base.ConfidenceSignal, AttentionSourceHelper)
	asked := desktopEvents.wait(t, tabID, "bell on the desktop", needsMe)
	phoneAsked := phoneEvents.wait(t, tabID, "bell on the phone", needsMe)
	if asked.Since == "" || phoneAsked.Since != asked.Since || asked.Seen {
		t.Fatalf("desktop %+v, phone %+v", asked, phoneAsked)
	}

	if err := desktop.MarkAttentionSeenFor(ctx, tabID, asked.Since); err != nil {
		t.Fatal(err)
	}
	seen := phoneEvents.wait(t, tabID, "seen on the phone", func(event AttentionEvent) bool { return event.Seen })
	if seen.State != base.SessionNeedsMe || seen.Since != asked.Since {
		t.Fatalf("phone seen state %+v", seen)
	}
	listed, err := phone.ListAttentionFor(ctx)
	if err != nil || len(listed) != 1 || !listed[0].Seen || listed[0].Source != AttentionSourceHelper {
		t.Fatalf("phone list %+v, %v", listed, err)
	}
}
