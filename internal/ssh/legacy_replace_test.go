package ssh

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

// legacyDaemonClient answers like a target whose state directory is still
// served by a protocol 2 helper with two sessions, until the bridge replaces
// it.
type legacyDaemonClient struct {
	mu       sync.Mutex
	replaced bool
	calls    []string
	result   error
}

var legacyTargetHelper = supervisor.RunningHelper{Protocol: 2, PID: 4242, Activity: supervisor.ActivitySnapshot{PTY: 1, Exec: 1}, ActivityKnown: true}

func (c *legacyDaemonClient) List(context.Context) ([]supervisor.Info, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.replaced {
		return nil, nil
	}
	return nil, &supervisor.LegacyHelperError{Helper: legacyTargetHelper, ConsentToken: supervisor.HelperConsentToken(legacyTargetHelper)}
}

func (c *legacyDaemonClient) RuntimeMetadata(context.Context) (supervisor.RuntimeMetadata, bool, error) {
	return supervisor.RuntimeMetadata{}, false, nil
}

func (c *legacyDaemonClient) RequireIdle(context.Context) error { return nil }

func (c *legacyDaemonClient) ReplaceLegacyHelper(_ context.Context, token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, token)
	if c.result != nil {
		return c.result
	}
	if token != supervisor.HelperConsentToken(legacyTargetHelper) {
		return &supervisor.LegacyHelperError{Helper: legacyTargetHelper, ConsentToken: supervisor.HelperConsentToken(legacyTargetHelper), Stale: true}
	}
	c.replaced = true
	return nil
}

func legacyTargetResolver(t *testing.T, client *legacyDaemonClient) (*Resolver, *fakeHost) {
	t.Helper()
	host := &fakeHost{
		discovery:    testDiscovery(t, t.TempDir()),
		pathBinary:   "/usr/bin/nexterm-helper",
		probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
		running:      true,
	}
	resolver := testResolver(host, "")
	resolver.RetryBackoff = -1
	resolver.clientFor = func(*Daemon) remoteDaemonClient { return client }
	return resolver, host
}

func TestEnsureDaemonReportsALegacyHelperWithoutStoppingIt(t *testing.T) {
	client := &legacyDaemonClient{}
	resolver, host := legacyTargetResolver(t, client)
	started := time.Now()
	_, err := resolver.EnsureDaemonWithOptions(t.Context(), nil, EnsureOptions{})
	var legacy *supervisor.LegacyHelperError
	if !errors.As(err, &legacy) || legacy.Helper.Activity.Total() != 2 || legacy.ConsentToken == "" {
		t.Fatalf("ensure beside a protocol 2 helper = %v, want its LegacyHelperError", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("legacy helper took %s to report", elapsed)
	}
	if len(client.calls) != 0 || host.sawCommand("setsid") {
		t.Fatalf("ensure without consent replaced %v or spawned a helper", client.calls)
	}
}

func TestEnsureDaemonReplacesALegacyHelperWithConsent(t *testing.T) {
	client := &legacyDaemonClient{}
	resolver, host := legacyTargetResolver(t, client)
	var stages []string
	resolver.Progress = func(_ context.Context, progress HelperProgress) { stages = append(stages, progress.Stage) }
	daemon, err := resolver.EnsureDaemonWithOptions(t.Context(), nil, EnsureOptions{ReplaceLegacy: supervisor.HelperConsentToken(legacyTargetHelper)})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 1 || !daemon.Update.ReplacedLegacy || daemon.Update.EndedSessions != 2 {
		t.Fatalf("replacement calls %v, update %+v; want one replacement ending 2 sessions", client.calls, daemon.Update)
	}
	if host.sawCommand("setsid") {
		t.Fatal("ensure spawned a helper beside the one the bridge started")
	}
	found := false
	for _, stage := range stages {
		found = found || stage == "replace_legacy"
	}
	if !found {
		t.Fatalf("progress %v does not show the replacement", stages)
	}
}

func TestEnsureDaemonReportsAFailedReplacementOnce(t *testing.T) {
	for name, result := range map[string]error{
		"start failed": &supervisor.HelperReplaceError{Stage: supervisor.ReplaceStart, RolledBack: true, Cause: errors.New("did not start")},
		"stale":        nil,
	} {
		t.Run(name, func(t *testing.T) {
			client := &legacyDaemonClient{result: result}
			resolver, host := legacyTargetResolver(t, client)
			token := supervisor.HelperConsentToken(legacyTargetHelper)
			if result == nil {
				stale := legacyTargetHelper
				stale.Activity = supervisor.ActivitySnapshot{}
				token = supervisor.HelperConsentToken(stale)
			}
			_, err := resolver.EnsureDaemonWithOptions(t.Context(), nil, EnsureOptions{ReplaceLegacy: token})
			if err == nil {
				t.Fatal("a failed replacement reported success")
			}
			var legacy *supervisor.LegacyHelperError
			if result == nil && (!errors.As(err, &legacy) || !legacy.Stale) {
				t.Fatalf("stale consent = %v, want a stale LegacyHelperError", err)
			}
			if result != nil && !errors.Is(err, supervisor.ErrHelperReplaceFailed) {
				t.Fatalf("failed replacement = %v, want ErrHelperReplaceFailed", err)
			}
			if len(client.calls) != 1 || host.sawCommand("setsid") {
				t.Fatalf("replacement calls %v (spawned %v); a failure must not be retried or followed by a start", client.calls, host.sawCommand("setsid"))
			}
		})
	}
}
