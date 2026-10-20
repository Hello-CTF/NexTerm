//go:build darwin || linux

package production

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
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

func TestProductionDefaultCloseRecoveredDeadTabDoesNotDependOnPump(t *testing.T) {
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
	response := dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","sessionId":"`+connected.ID+`"}`, "", "client-a")
	requireProductionNull(t, response)
	release()

	if _, err := production.Services.Sessions.Tab(tabID); !errors.Is(err, session.ErrTabNotFound) {
		t.Fatalf("default close retained recovered dead tab until its pump ran: %v", err)
	}
	infos, err := fixture.client.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.ID == tabID {
			t.Fatalf("default close left the dead durable descriptor behind: %+v", info)
		}
	}
}
