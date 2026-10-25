//go:build darwin || linux

package production

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// stalledResolver keeps the helper deployment in progress until released.
type stalledResolver struct{ release chan struct{} }

func (r stalledResolver) ResolveDurable(ctx context.Context, _ base.Transport) (base.DurableProvider, error) {
	select {
	case <-r.release:
	case <-ctx.Done():
	}
	return nil, context.Canceled
}

func TestTerminalAttachDoesNotWaitForHelperInstall(t *testing.T) {
	resolver := stalledResolver{release: make(chan struct{})}
	t.Cleanup(func() { close(resolver.release) })
	production, _ := newRemoteRecoveryHarness(t, resolver)
	connected := connectRemoteSSHAsset(t, production)

	started := time.Now()
	response := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "install-in-progress", "client-a")
	elapsed := time.Since(started)
	var attached attachedTabDTO
	requireStoreTestResponse(t, response, &attached)
	tab, err := production.Services.Sessions.Tab(attached.TabID)
	if err != nil || tab.Info().Durable {
		t.Fatalf("tab = %+v, %v; want a regular PTY while the helper installs", tab, err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("attach took %v while the helper was still installing", elapsed)
	}
}
