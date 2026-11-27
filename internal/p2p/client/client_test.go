package client

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/p2p/agentside"
	"github.com/Hello-CTF/NexTerm/internal/p2p/legtest"
	"github.com/Hello-CTF/NexTerm/internal/p2p/natprobe"
	"github.com/Hello-CTF/NexTerm/internal/p2p/selector"
	"github.com/Hello-CTF/NexTerm/internal/p2p/signal"
)

const transferSize = 2 << 20

// A stream starts on the relay, moves to the direct leg mid-transfer, and every byte arrives exactly once.
func TestMigratesToDirectMidTransfer(t *testing.T) {
	h := newHarness(t)
	stream := h.dial("terminal")
	tr := startTransfer(t, stream, transferSize)
	tr.write(t, 0, transferSize/2)
	h.waitPath(PathDirect, "")
	h.waitDirect(1)
	tr.write(t, transferSize/2, transferSize)
	tr.finish(t)
	h.mu.Lock()
	resumes := h.resumes
	h.mu.Unlock()
	if resumes == 0 {
		t.Fatal("the agent saw no resume: the stream never moved")
	}
	// New streams open on the preferred leg.
	h.mu.Lock()
	relayDials := h.relayDials
	h.mu.Unlock()
	second := h.dial("forward")
	h.mu.Lock()
	if h.relayDials != relayDials {
		t.Fatal("a new stream used the relay while the direct leg is preferred")
	}
	h.mu.Unlock()
	if _, direct := h.mgr.Counts(testDevice); direct != 2 {
		t.Fatalf("direct streams = %d, want 2", direct)
	}
	_ = second.Close()
	_ = stream.Close()
}

// A leg that dies mid-transfer leaves the stream on the relay without loss.
func TestLegDeathFallsBackToRelay(t *testing.T) {
	h := newHarness(t)
	stream := h.dial("terminal")
	h.waitDirect(1)
	tr := startTransfer(t, stream, transferSize)
	tr.write(t, 0, transferSize/3)
	for _, l := range h.net.Legs() {
		_ = l.Close()
	}
	tr.write(t, transferSize/3, transferSize)
	tr.finish(t)
	h.waitPath(PathRelay, ReasonConnectFailed)
	h.waitDirect(0)
	// The next attempt waits out the backoff.
	dials, _ := h.net.Dials()
	h.tick(time.Minute)
	if again, _ := h.net.Dials(); again != dials {
		t.Fatalf("leg dials went from %d to %d inside the backoff", dials, again)
	}
	h.tickUntilDirect(1, BackoffMin+selector.RecoverHealthy+selector.MinDwell)
	_ = stream.Close()
}

// A revoke on the agent ends the session; the streams resume on the relay.
func TestRevokeFallsBackToRelay(t *testing.T) {
	h := newHarness(t)
	stream := h.dial("terminal")
	h.waitDirect(1)
	tr := startTransfer(t, stream, transferSize)
	tr.write(t, 0, transferSize/2)
	h.svc.Revoke("grant-1", "session_revoked")
	tr.write(t, transferSize/2, transferSize)
	tr.finish(t)
	h.waitPath(PathRelay, ReasonServerDisabled)
	h.waitDirect(0)
	if h.svc.Sessions() != 0 {
		t.Fatal("the agent kept the revoked session")
	}
	_ = stream.Close()
}

// An agent refuses a re-attach to a session it already ended with "refused"; that says nothing about the
// server's policy, so the fallback reports a failed connection.
func TestRefusedReattachIsConnectFailed(t *testing.T) {
	h := newHarness(t)
	stream := h.dial("terminal")
	h.waitDirect(1)
	d := h.mgr.existing(testDevice)
	d.post(event{kind: evSignalClose, sess: d.currentSession(), reason: signal.CloseRefused})
	h.waitPath(PathRelay, ReasonConnectFailed)
	h.waitDirect(0)
	_ = stream.Close()
}

// relay -> auto -> direct -> relay, each applied to the live stream without reconnecting.
func TestModeSwitches(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.mode = selector.ModeRelay })
	stream := h.dial("terminal")
	h.waitPath(PathRelay, ReasonModeRelay)
	if dials, _ := h.net.Dials(); dials != 0 {
		t.Fatalf("relay mode dialed %d legs", dials)
	}
	h.set(func(h *harness) { h.mode = selector.ModeAuto })
	h.mgr.ModeChanged(testDevice)
	h.waitDirect(1)
	if status := h.mgr.Status(context.Background(), testDevice); status.Mode != "auto" || status.Path != PathDirect {
		t.Fatalf("auto status = %+v", status)
	}
	h.set(func(h *harness) { h.mode = selector.ModeDirect })
	h.mgr.ModeChanged(testDevice)
	h.waitFor("mode direct", func() bool { return h.mgr.Status(context.Background(), testDevice).Mode == "direct" })
	// Direct mode ignores loss that would degrade auto.
	for _, l := range h.net.Legs() {
		l.SetLoss(0.5)
	}
	h.tick(2 * selector.MinDwell)
	if status := h.mgr.Status(context.Background(), testDevice); status.Path != PathDirect {
		t.Fatalf("direct mode left the leg on loss: %+v", status)
	}
	tr := startTransfer(t, stream, transferSize)
	tr.write(t, 0, transferSize/2)
	h.set(func(h *harness) { h.mode = selector.ModeRelay })
	h.mgr.ModeChanged(testDevice)
	tr.write(t, transferSize/2, transferSize)
	tr.finish(t)
	h.waitPath(PathRelay, ReasonModeRelay)
	h.waitDirect(0)
	h.waitFor("agent session closed", func() bool { return h.svc.Sessions() == 0 })
	_ = stream.Close()
}

// In auto mode, sustained loss moves the streams to the relay after the dwell, and they return after the leg
// stays healthy.
func TestDegradesOnLossAndRecovers(t *testing.T) {
	h := newHarness(t)
	stream := h.dial("terminal")
	h.waitDirect(1)
	legs := h.net.Legs()
	legs[len(legs)-1].SetLoss(0.2)
	h.tickUntil("degradation", 2*selector.MinDwell, func() bool {
		status := h.mgr.Status(context.Background(), testDevice)
		return status.Path == PathRelay && status.Reason == ReasonUnstable
	})
	if elapsed := h.clock.Now().Sub(testEpoch); elapsed < selector.MinDwell {
		t.Fatalf("degraded after %s, inside the dwell", elapsed)
	}
	h.waitDirect(0)
	degradedAt := h.clock.Now()
	legs[len(legs)-1].SetLoss(0)
	h.tickUntilDirect(1, 2*(selector.MinDwell+selector.RecoverHealthy))
	if back := h.clock.Now().Sub(degradedAt); back < selector.MinDwell {
		t.Fatalf("returned after %s, inside the dwell", back)
	}
	_ = stream.Close()
}

// Grants renew through the signal bridge before they expire; a renewal the server refuses moves the streams to
// the relay before the agent's grace runs out.
func TestGrantRenewalAndExpiry(t *testing.T) {
	h := newHarness(t)
	stream := h.dial("terminal")
	h.waitDirect(1)
	original, ok := h.svc.SessionExpiry("grant-1")
	if !ok {
		t.Fatal("no agent session")
	}
	// The renewal is due after renew_after_ms; the agent gets the new grant over the signal bridge.
	h.clock.Advance(2 * time.Minute)
	h.waitFor("renewed grant on the agent", func() bool {
		expiry, ok := h.svc.SessionExpiry("grant-1")
		return ok && expiry.After(original)
	})
	// The original grant expired and its grace passed: the agent still holds the session.
	h.clock.Advance(original.Sub(h.clock.Now()) + agentside.Grace + time.Second)
	if h.svc.Sessions() != 1 {
		t.Fatal("the agent ended a renewed session")
	}
	if status := h.mgr.Status(context.Background(), testDevice); status.Path != PathDirect {
		t.Fatalf("status after renewals = %+v", status)
	}
	// Renewals the server refuses move the streams to the relay before the grant runs out.
	h.set(func(h *harness) { h.grantErr = errForbidden })
	h.tickUntil("fallback after refused renewals", grantTTLForTest+time.Minute, func() bool {
		status := h.mgr.Status(context.Background(), testDevice)
		return status.Path == PathRelay && status.Reason == ReasonServerDisabled
	})
	h.waitDirect(0)
	h.waitFor("agent session closed", func() bool { return h.svc.Sessions() == 0 })
	_ = stream.Close()
}

const grantTTLForTest = 5 * time.Minute

// A proxy keeps the device on the relay, as do an unverified server connection, a disabled server, a pending
// device key and the background; a network change or return to the foreground retries at once.
func TestSkipReasons(t *testing.T) {
	cases := []struct {
		name   string
		apply  func(*harness)
		reason string
	}{
		{"proxy", func(h *harness) { h.proxied = true }, ReasonProxy},
		{"insecure", func(h *harness) { h.insecure = true }, ReasonInsecure},
		{"server disabled", func(h *harness) { h.enabled = false }, ReasonServerDisabled},
		{"key pending", func(h *harness) { h.keyState = agent.P2PKeyStatePending }, ReasonKeyPending},
		{"device unsupported", func(h *harness) { h.keyState = "" }, ReasonDeviceUnsupported},
		{"grant offline", func(h *harness) { h.grantErr = offlineErr }, ReasonOffline},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.apply)
			stream := h.dial("terminal")
			h.waitPath(PathRelay, tc.reason)
			if dials, _ := h.net.Dials(); dials != 0 {
				t.Fatalf("dialed %d legs", dials)
			}
			if tc.reason == ReasonProxy || tc.reason == ReasonInsecure {
				h.mu.Lock()
				grants := h.grants
				h.mu.Unlock()
				if grants != 0 {
					t.Fatalf("requested %d grants", grants)
				}
			}
			_ = stream.Close()
		})
	}
}

func TestBackgroundAndNetworkChange(t *testing.T) {
	h := newHarness(t)
	stream := h.dial("terminal")
	h.waitDirect(1)
	h.mgr.SetBackground(true)
	h.waitPath(PathRelay, ReasonBackground)
	h.waitDirect(0)
	if dials, _ := h.net.Dials(); dials != 1 {
		t.Fatalf("leg dials in the background = %d", dials)
	}
	h.mgr.SetBackground(false)
	h.tickUntilDirect(1, ForegroundDelay+selector.RecoverHealthy+selector.MinDwell)

	// A failed attempt backs off; a network change retries at once.
	h.net.SetFail(errors.New("no path"))
	for _, l := range h.net.Legs() {
		_ = l.Close()
	}
	h.waitPath(PathRelay, ReasonConnectFailed)
	h.clock.Advance(BackoffMin)
	h.waitFor("the failed attempt", func() bool { dials, _ := h.net.Dials(); return dials == 3 })
	h.net.SetFail(nil)
	h.mgr.NetworkChanged()
	h.waitFor("the retry after the network change", func() bool { dials, _ := h.net.Dials(); return dials >= 4 })
	h.tickUntilDirect(1, selector.RecoverHealthy+selector.MinDwell)
	_ = stream.Close()
}

// With every STUN server unreachable the probe reports blocked, the attempt is host-only, and the relay stream
// works meanwhile.
func TestUnreachableSTUNIsHostOnly(t *testing.T) {
	release := make(chan struct{})
	probed := make(chan time.Duration, 1)
	h := newHarness(t, func(h *harness) {
		h.stun = []agent.P2PEndpoint{{Host: "127.0.0.1", Port: 9}, {Host: "127.0.0.1", Port: 19}}
		h.probe = func(ctx context.Context, servers []string) natprobe.Mapping {
			<-release
			started := time.Now()
			mapping := defaultProbe(ctx, servers)
			probed <- time.Since(started)
			return mapping
		}
	})
	h.net.SetFail(errors.New("host candidates only"))
	// The relay stream works while the probe has not even started.
	stream := h.dial("terminal")
	tr := startTransfer(t, stream, 64<<10)
	tr.write(t, 0, 64<<10)
	tr.finish(t)
	close(release)
	if took := <-probed; took > natprobe.DefaultTimeout+2*time.Second {
		t.Fatalf("the probe of unreachable servers took %s, more than its %s timeout", took, natprobe.DefaultTimeout)
	}
	h.waitFor("host-only attempt", func() bool { dials, _ := h.net.Dials(); return dials > 0 })
	_, configs := h.net.Dials()
	if !configs[0].HostOnly {
		t.Fatalf("leg config = %+v, want host-only", configs[0])
	}
	if !slices.Equal(configs[0].STUN, []string{"127.0.0.1:9", "127.0.0.1:19"}) {
		t.Fatalf("STUN = %v", configs[0].STUN)
	}
	h.waitPath(PathRelay, ReasonConnectFailed)
	diag := h.mgr.Diagnostics(context.Background(), testDevice)
	if !diag.Probed || diag.LocalNAT != string(natprobe.Blocked) || diag.STUNReachable {
		t.Fatalf("diagnostics = %+v", diag)
	}
	_ = stream.Close()
}

// Streams outside the grant scope stay on the relay; every direct stream is reported to the audit.
func TestScopeAndAudit(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.scope = []string{"terminal"} })
	terminal := h.dial("terminal")
	h.waitDirect(1)
	forward := h.dial("forward")
	if total, direct := h.mgr.Counts(testDevice); total != 2 || direct != 1 {
		t.Fatalf("streams total %d direct %d, want the forward stream on the relay", total, direct)
	}
	tr := startTransfer(t, terminal, 4096)
	tr.write(t, 0, 4096)
	tr.finish(t)
	_ = terminal.Close()
	_ = forward.Close()
	h.waitFor("streams closed", func() bool { total, _ := h.mgr.Counts(testDevice); return total == 0 })
	h.waitFor("audit upload", func() bool {
		h.clock.Advance(agentside.AuditFlushInterval)
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.audits) > 0
	})
	h.mu.Lock()
	record := h.audits[0]
	h.mu.Unlock()
	if record.GrantID != "grant-1" || record.Purpose != "terminal" || record.Outcome != "" || record.BytesIn < 4096 || record.BytesOut < 4096 || record.EndedAt < record.StartedAt {
		t.Fatalf("audit record = %+v", record)
	}
}

// Diagnostics report the selected pair, RTT and policy.
func TestDiagnostics(t *testing.T) {
	h := newHarness(t)
	stream := h.dial("terminal")
	h.waitDirect(1)
	diag := h.mgr.Diagnostics(context.Background(), testDevice)
	if diag.Status.Path != PathDirect || diag.SelectedPair == nil || diag.DirectRTTMS == nil || diag.Policy != (P2PPolicy{Server: PolicyOn, Device: PolicyOn, DeviceKey: agent.P2PKeyStateBound, Mode: "auto"}) {
		t.Fatalf("diagnostics = %+v", diag)
	}
	if len(diag.RecentSwitches) == 0 || diag.RecentSwitches[len(diag.RecentSwitches)-1].To != PathDirect {
		t.Fatalf("switches = %+v", diag.RecentSwitches)
	}
	_ = stream.Close()
}

// A device that never dialed reports each policy layer as read now, and the layer that rules out a direct
// connection as its reason; nothing was probed.
func TestDiagnosticsPolicyLayers(t *testing.T) {
	readErr := ipc.NewError(ipc.CodeInternal, "server unreachable")
	cases := []struct {
		name      string
		configure func(*harness)
		want      P2PPolicy
		reason    string
	}{
		{"bound", func(*harness) {}, P2PPolicy{Server: PolicyOn, Device: PolicyOn, DeviceKey: agent.P2PKeyStateBound}, ReasonNone},
		{"server disabled", func(h *harness) { h.enabled = false }, P2PPolicy{Server: PolicyOff, Device: PolicyOn, DeviceKey: agent.P2PKeyStateBound}, ReasonServerDisabled},
		{"key pending", func(h *harness) { h.keyState = agent.P2PKeyStatePending }, P2PPolicy{Server: PolicyOn, Device: PolicyOff, DeviceKey: agent.P2PKeyStatePending}, ReasonKeyPending},
		{"key none", func(h *harness) { h.keyState = agent.P2PKeyStateNone }, P2PPolicy{Server: PolicyOn, Device: PolicyOff, DeviceKey: agent.P2PKeyStateNone}, ReasonDeviceUnsupported},
		{"no key reported", func(h *harness) { h.keyState = "" }, P2PPolicy{Server: PolicyOn, Device: PolicyOff}, ReasonDeviceUnsupported},
		{"unreadable", func(h *harness) { h.configErr, h.keyErr = readErr, readErr }, P2PPolicy{Server: PolicyUnknown, Device: PolicyUnknown}, ReasonNone},
		{"relay mode", func(h *harness) { h.mode, h.enabled = selector.ModeRelay, false }, P2PPolicy{Server: PolicyOff, Device: PolicyOn, DeviceKey: agent.P2PKeyStateBound, Mode: "relay"}, ReasonModeRelay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.configure)
			diag := h.mgr.Diagnostics(context.Background(), testDevice)
			want := tc.want
			if want.Mode == "" {
				want.Mode = "auto"
			}
			if diag.Policy != want || diag.Status.Path != PathRelay || diag.Status.Reason != tc.reason || diag.Probed {
				t.Fatalf("diagnostics = %+v, want policy %+v and reason %q", diag, want, tc.reason)
			}
		})
	}
}

// A dialed device whose policy can no longer be read keeps the layers its attempt saw instead of reporting off.
func TestDiagnosticsKeepPolicyOnReadError(t *testing.T) {
	h := newHarness(t)
	stream := h.dial("terminal")
	h.waitDirect(1)
	readErr := ipc.NewError(ipc.CodeInternal, "server unreachable")
	h.mgr.forgetConfig()
	h.set(func(h *harness) { h.configErr, h.keyErr = readErr, readErr })
	diag := h.mgr.Diagnostics(context.Background(), testDevice)
	if diag.Policy.Server != PolicyOn || diag.Policy.Device != PolicyOn || diag.Policy.DeviceKey != agent.P2PKeyStateBound {
		t.Fatalf("policy = %+v", diag.Policy)
	}
	_ = stream.Close()
}

var _ = legtest.ErrPingLost
