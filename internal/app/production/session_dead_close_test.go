//go:build darwin || linux

package production

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

type gatedRecoveryProvider struct {
	*supervisor.RemoteProvider
	gate <-chan struct{}
}

func (p *gatedRecoveryProvider) AttachWithIdentity(ctx context.Context, id string, identity base.DurableIdentity) (base.DurableAttachment, error) {
	attachment, err := p.RemoteProvider.AttachWithIdentity(ctx, id, identity)
	if attachment == nil || err != nil {
		return attachment, err
	}
	return &gatedDurableAttachment{DurableAttachment: attachment, gate: p.gate}, nil
}

type gatedDurableAttachment struct {
	base.DurableAttachment
	gate <-chan struct{}
}

func (a *gatedDurableAttachment) Read(data []byte) (int, error) {
	<-a.gate
	return a.DurableAttachment.Read(data)
}

func TestProductionKillRecoveredDeadTabDoesNotDependOnPump(t *testing.T) {
	fixture := newRemoteDaemonFixture(t)
	gate := make(chan struct{})
	provider := &gatedRecoveryProvider{RemoteProvider: fixture.provider, gate: gate}
	production, _ := newRemoteRecoveryHarness(t, &recordingDurableResolver{provider: provider})
	connected := connectRemoteSSHAsset(t, production)
	tabID := ids.New()
	attachment, err := fixture.provider.Create(t.Context(), base.DurableCreateOptions{
		ID: tabID, Command: []string{"/bin/sh", "-c", "exit 23"}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := attachment.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		infos, err := fixture.client.List(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		dead := false
		for _, info := range infos {
			if info.ID == tabID && info.Dead && info.ExitCode != nil && *info.ExitCode == 23 {
				dead = true
			}
		}
		if dead {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("durable process did not reach authoritative dead state: %+v", infos)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := production.Services.Sessions.Tab(tabID); !errors.Is(err, session.ErrTabNotFound) {
		t.Fatalf("test setup unexpectedly has a managed tab: %v", err)
	}

	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	defer release()
	response := dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","sessionId":"`+connected.ID+`","mode":"kill"}`, "", "client-a")
	requireProductionNull(t, response)
	release()

	if _, err := production.Services.Sessions.Tab(tabID); !errors.Is(err, session.ErrTabNotFound) {
		t.Fatalf("explicit kill retained recovered dead tab until its pump ran: %v", err)
	}
	infos, err := fixture.client.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.ID == tabID {
			t.Fatalf("explicit kill left the dead durable descriptor behind: %+v", info)
		}
	}
}

func TestProductionCloseMissingLocalTabDoesNotRecoverRemote(t *testing.T) {
	for _, mode := range []string{"", "detach"} {
		t.Run("mode="+mode, func(t *testing.T) {
			fixture := newRemoteDaemonFixture(t)
			resolver := &recordingDurableResolver{provider: fixture.provider}
			production, _ := newRemoteRecoveryHarness(t, resolver)
			connected := connectRemoteSSHAsset(t, production)
			tabID := ids.New()
			attachment, err := fixture.provider.Create(t.Context(), base.DurableCreateOptions{
				ID: tabID, Command: []string{"sh"}, Cols: 80, Rows: 24,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := attachment.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := production.Services.Sessions.Tab(tabID); !errors.Is(err, session.ErrTabNotFound) {
				t.Fatalf("test setup unexpectedly has a managed tab: %v", err)
			}

			response := dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","sessionId":"`+connected.ID+`","mode":"`+mode+`"}`, "", "client-a")
			if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
				t.Fatalf("close with unknown remote survival = %+v, want visible not_found", response)
			}
			if calls := len(resolver.recorded()); calls != 0 {
				t.Fatalf("close triggered %d helper resolutions, want no recovery or redeployment", calls)
			}
			infos, err := fixture.client.List(t.Context())
			if err != nil || len(infos) != 1 || infos[0].ID != tabID || infos[0].Dead {
				t.Fatalf("close disturbed the original durable session: %+v, %v", infos, err)
			}
			connectedInfo, err := production.Services.Sessions.Session(connected.ID)
			if err != nil || connectedInfo.Info().Status != session.StatusConnected {
				t.Fatalf("close disconnected SSH: %+v, %v", connectedInfo, err)
			}
		})
	}
}

func TestProductionOrdinaryFallbackStartsDistinctShell(t *testing.T) {
	fixture := newRemoteDaemonFixture(t)
	originalID := ids.New()
	directory := t.TempDir()
	originalPIDPath := filepath.Join(directory, "original.pid")
	attachment, err := fixture.provider.Create(t.Context(), base.DurableCreateOptions{
		ID: originalID, Command: []string{"sh"}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attachment.Write([]byte("printf '%s' \"$$\" > '" + originalPIDPath + "'; printf 'original-''ready\\n'\n")); err != nil {
		t.Fatal(err)
	}
	readUntilDurableOutput(t, attachment, "original-ready")
	if err := attachment.Close(); err != nil {
		t.Fatal(err)
	}
	originalPID, err := os.ReadFile(originalPIDPath)
	if err != nil || len(originalPID) == 0 {
		t.Fatalf("original durable PID = %q, %v", originalPID, err)
	}

	transport := local.NewWithConfig(local.Config{Shell: "/bin/sh"})
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return transport, nil
		}),
		DurableResolver: &recordingDurableResolver{err: durable.ErrUnavailable},
		OpenAccess:      true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "ordinary-fallback", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := manager.OpenTab(t.Context(), session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "ordinary-fallback-channel", Cols: 80, Rows: 24,
		Durable: &session.DurableTabOptions{}, DurableFallback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened.Durable || opened.ID == originalID {
		t.Fatalf("ordinary fallback = %+v, want a distinct non-durable tab", opened)
	}
	ordinaryPIDPath := filepath.Join(directory, "ordinary.pid")
	if err := manager.Write(t.Context(), opened.ID, "client-a", []byte("printf '%s' \"$$\" > '"+ordinaryPIDPath+"'\n")); err != nil {
		t.Fatal(err)
	}
	var ordinaryPID []byte
	deadline := time.Now().Add(5 * time.Second)
	for {
		ordinaryPID, err = os.ReadFile(ordinaryPIDPath)
		if err == nil && len(ordinaryPID) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ordinary shell did not report its PID: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if string(ordinaryPID) == string(originalPID) {
		t.Fatalf("ordinary fallback reused original shell PID %q", ordinaryPID)
	}
	infos, err := fixture.client.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	alive := false
	for _, info := range infos {
		if info.ID == originalID && !info.Dead {
			alive = true
		}
	}
	if !alive {
		t.Fatalf("ordinary fallback killed the original durable session: %+v", infos)
	}
}
