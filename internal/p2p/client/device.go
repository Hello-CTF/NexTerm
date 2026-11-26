package client

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/p2p/clock"
	"github.com/Hello-CTF/NexTerm/internal/p2p/natprobe"
	"github.com/Hello-CTF/NexTerm/internal/p2p/selector"
	"github.com/Hello-CTF/NexTerm/internal/p2p/signal"
	"github.com/Hello-CTF/NexTerm/internal/rstream"
)

type eventKind int

const (
	evKick eventKind = iota
	evMode
	evBackground
	evForeground
	evNetwork
	evConnected
	evConnectFailed
	evLegDown
	evPingTick
	evPing
	evRelayTick
	evPong
	evRenewTick
	evRenewed
	evRenewFailed
	evExpiry
	evMigrated
	evSignalDown
	evSignalUp
	evSignalClose
	evStreamGone
	evIdle
	evRaw
)

type event struct {
	kind       eventKind
	sess       *directSession
	reason     string
	ok         bool
	rtt        time.Duration
	seq        uint64
	grant      *Grant
	gen        uint64
	closeAfter bool
	sig        *signal.Conn
}

// liveStream is one device stream and the path its carrier is on.
type liveStream struct {
	stream  *rstream.Stream
	purpose string
	path    atomic.Value // string
}

func (ls *liveStream) onPath() string {
	path, _ := ls.path.Load().(string)
	return path
}

// device is the path state of one device. The loop goroutine owns the decisions; mu guards what Dial, the
// resume hooks and the status calls read.
type device struct {
	m      *Manager
	id     string
	ctx    context.Context
	cancel context.CancelFunc
	events chan event
	done   chan struct{}
	once   sync.Once

	mu                  sync.Mutex
	streams             map[*rstream.Stream]*liveStream
	session             *directSession
	direct              bool
	st                  DevicePathStatus
	switches            []PathSwitch
	localNAT, remoteNAT string
	stunOK              bool
	directRTT, relayRTT time.Duration
	loss                float64
	hasLoss             bool
	serverOn, deviceOn  bool

	// Loop-owned state.
	sel          selector.Selector
	connecting   bool
	backoff      time.Duration
	retryAt      time.Time
	failReason   string
	timers       map[eventKind]clock.Timer
	migrateGen   atomic.Uint64
	pingInFlight bool
	// leaving is the session whose streams are moving to the relay before it closes.
	leaving *directSession
	// networkChanged marks a network change during an attempt.
	networkChanged bool
	// lastDropped is the session that just died, whose close frame may still arrive.
	lastDropped *directSession
	pingSeq     uint64
	pingSent    map[uint64]time.Time
	renewFails  int
}

func newDevice(m *Manager, id string, ctx context.Context) *device {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	d := &device{
		m: m, id: id, ctx: ctx, cancel: cancel, events: make(chan event, 64), done: make(chan struct{}),
		streams: make(map[*rstream.Stream]*liveStream), timers: make(map[eventKind]clock.Timer), pingSent: make(map[uint64]time.Time),
		st:       DevicePathStatus{DeviceID: id, Mode: string(selector.ModeAuto), Path: PathRelay, Since: m.clock.Now().UnixMilli()},
		localNAT: string(natprobe.Unknown), remoteNAT: string(natprobe.Unknown),
	}
	m.wg.Add(1)
	go d.run()
	return d
}

func (d *device) post(ev event) {
	select {
	case d.events <- ev:
	case <-d.done:
	}
}

func (d *device) stop() {
	d.once.Do(func() { close(d.done) })
}

func (d *device) run() {
	defer d.m.wg.Done()
	for {
		select {
		case ev := <-d.events:
			d.handle(ev)
		case <-d.done:
			d.shutdown()
			return
		}
	}
}

func (d *device) shutdown() {
	for _, timer := range d.timers {
		timer.Stop()
	}
	d.mu.Lock()
	sess := d.session
	d.session, d.direct = nil, false
	d.mu.Unlock()
	if sess != nil {
		sess.close()
	}
	d.cancel()
}

// after schedules ev once; a pending event of the same kind is replaced.
func (d *device) after(delay time.Duration, ev event) {
	if timer := d.timers[ev.kind]; timer != nil {
		timer.Stop()
	}
	d.timers[ev.kind] = d.m.clock.AfterFunc(delay, func() { d.post(ev) })
}

func (d *device) cancelTimer(kind eventKind) {
	if timer := d.timers[kind]; timer != nil {
		timer.Stop()
		delete(d.timers, kind)
	}
}

func (d *device) status() DevicePathStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st
}

func (d *device) diagnostics() DeviceP2PDiagnostics {
	d.mu.Lock()
	defer d.mu.Unlock()
	diag := DeviceP2PDiagnostics{
		Status: d.st, LocalNAT: d.localNAT, RemoteNAT: d.remoteNAT, STUNReachable: d.stunOK,
		RecentSwitches: append([]PathSwitch{}, d.switches...),
		Policy:         P2PPolicy{Server: d.serverOn, Device: d.deviceOn, Mode: d.st.Mode},
	}
	ms := func(value time.Duration) *float64 {
		if value <= 0 {
			return nil
		}
		result := float64(value) / float64(time.Millisecond)
		return &result
	}
	diag.DirectRTTMS, diag.RelayRTTMS = ms(d.directRTT), ms(d.relayRTT)
	if d.session != nil {
		stats := d.session.leg.Stats()
		if stats.Local.Address != "" || stats.Remote.Address != "" {
			diag.SelectedPair = &P2PSelectedPair{
				Local:  net.JoinHostPort(stats.Local.Address, fmt.Sprint(stats.Local.Port)),
				Remote: net.JoinHostPort(stats.Remote.Address, fmt.Sprint(stats.Remote.Port)),
				Type:   stats.Local.Type + "/" + stats.Remote.Type,
			}
		}
		if d.hasLoss {
			loss := d.loss * 100
			diag.LossPct = &loss
		}
	}
	return diag
}

// setStatus records the path and reason; a path change is a switch with a new Since.
func (d *device) setStatus(path, reason string, mode selector.Mode) {
	now := d.m.clock.Now().UnixMilli()
	d.mu.Lock()
	previous := d.st
	next := previous
	next.Path, next.Reason, next.Mode = path, reason, string(mode)
	if path != previous.Path {
		next.Since = now
		d.switches = append(d.switches, PathSwitch{At: now, From: previous.Path, To: path, Reason: reason})
		if len(d.switches) > RecentSwitches {
			d.switches = d.switches[len(d.switches)-RecentSwitches:]
		}
	}
	d.st = next
	d.mu.Unlock()
	if next != previous && d.m.opts.Emit != nil {
		d.m.opts.Emit(next)
	}
}

func (d *device) readMode() selector.Mode {
	mode, err := d.m.backend.Mode(d.ctx, d.id)
	if err != nil {
		// An unreadable store keeps the previous choice.
		d.mu.Lock()
		defer d.mu.Unlock()
		return selector.ParseMode(d.st.Mode)
	}
	return selector.ParseMode(string(mode))
}

// policyReason is why no direct connection may be used now, from local policy only.
func (d *device) policyReason(mode selector.Mode) string {
	switch {
	case d.m.inBackground():
		return ReasonBackground
	case mode == selector.ModeRelay:
		return ReasonModeRelay
	}
	if endpoint, err := d.m.backend.Endpoint(d.ctx); err == nil {
		return endpointReason(endpoint)
	}
	return ""
}

func (d *device) handle(ev event) {
	switch ev.kind {
	case evKick, evMode:
		d.evaluate()
	case evBackground:
		d.closeSession(ReasonBackground)
		d.evaluate()
	case evForeground:
		d.resetBackoff()
		d.after(d.m.opts.ForegroundDelay, event{kind: evKick})
	case evNetwork:
		d.resetBackoff()
		// An attempt that started on the old network retries at once when it fails.
		d.networkChanged = d.connecting
		d.evaluate()
	case evConnected:
		d.connected(ev.sess)
	case evConnectFailed:
		d.connecting = false
		d.failed(ev.reason)
		if d.networkChanged {
			d.networkChanged = false
			d.resetBackoff()
		}
		d.evaluate()
	case evLegDown:
		d.legDown(ev.sess)
	case evPingTick:
		d.pingTick()
	case evPing:
		d.pingResult(ev)
	case evRelayTick:
		d.relayTick()
	case evPong:
		if sent, ok := d.pingSent[ev.seq]; ok {
			delete(d.pingSent, ev.seq)
			d.mu.Lock()
			d.relayRTT = d.m.clock.Now().Sub(sent)
			d.mu.Unlock()
		}
	case evRenewTick:
		d.renew()
	case evRenewed:
		d.renewed(ev)
	case evRenewFailed:
		d.renewFailed(ev)
	case evExpiry:
		if ev.sess == d.currentSession() {
			d.m.logger.Info("direct grant expired without renewal", "device_id", d.id)
			d.leave(ev.reason, true)
		}
	case evMigrated:
		if ev.closeAfter && ev.gen == d.migrateGen.Load() && ev.sess != nil && ev.sess == d.currentSession() {
			d.dropSession(ev.sess)
			d.evaluate()
		}
	case evSignalDown:
		if ev.sess == d.currentSession() && ev.sess.signal() == ev.sig {
			// The leg outlives its signal bridge; a renewal re-attaches again if this attempt fails.
			ev.sess.setSignal(nil)
			go d.reattach(ev.sess)
		}
	case evSignalUp:
		if ev.sess == d.currentSession() {
			ev.sess.setSignal(ev.sig)
		} else {
			_ = ev.sig.Close()
		}
	case evSignalClose:
		switch {
		case ev.sess == d.currentSession():
			ev.sess.setCloseReason(ev.reason)
			d.legDown(ev.sess)
		case ev.sess == d.lastDropped && closeReason(ev.reason) != "":
			// The agent closed the leg before its close frame arrived: correct the reason.
			d.failReason = closeReason(ev.reason)
			if status := d.status(); status.Path == PathRelay {
				d.setStatus(PathRelay, d.failReason, selector.ParseMode(status.Mode))
			}
		}
	case evStreamGone:
		d.mu.Lock()
		empty := len(d.streams) == 0
		d.mu.Unlock()
		if empty {
			d.after(d.m.opts.IdleClose, event{kind: evIdle})
		}
	case evRaw:
		if d.currentSession() == nil && !d.connecting {
			d.failReason = ReasonDeviceUnsupported
			d.setStatus(PathRelay, ReasonDeviceUnsupported, d.readMode())
		}
	case evIdle:
		d.mu.Lock()
		empty := len(d.streams) == 0
		d.mu.Unlock()
		if empty {
			d.closeSession("")
			d.setStatus(PathRelay, "", d.readMode())
		}
	}
}

func (d *device) currentSession() *directSession {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.session
}

func (d *device) resetBackoff() {
	d.backoff, d.retryAt = 0, time.Time{}
	d.cancelTimer(evKick)
}

// failed records a failed attempt and schedules the next one with backoff.
func (d *device) failed(reason string) {
	if d.backoff == 0 {
		d.backoff = d.m.opts.BackoffMin
	} else {
		d.backoff = min(2*d.backoff, d.m.opts.BackoffMax)
	}
	d.failReason = reason
	d.retryAt = d.m.clock.Now().Add(d.backoff)
	d.after(d.backoff, event{kind: evKick})
}

// evaluate applies policy and starts an attempt when none runs.
func (d *device) evaluate() {
	mode := d.readMode()
	if reason := d.policyReason(mode); reason != "" {
		d.cancelTimer(evKick)
		if d.currentSession() != nil {
			// The user's relay choice keeps make-before-break; going to the background does not wait.
			d.leave(reason, true)
		} else {
			d.setStatus(PathRelay, reason, mode)
		}
		return
	}
	d.mu.Lock()
	streams := len(d.streams)
	d.mu.Unlock()
	if sess := d.currentSession(); sess != nil {
		if sess == d.leaving {
			return
		}
		d.apply(d.sel.Decide(d.m.clock.Now(), d.sample(sess, mode, false, false, 0)), sess, mode)
		return
	}
	if d.connecting {
		return
	}
	if streams == 0 {
		d.setStatus(PathRelay, d.failReason, mode)
		return
	}
	if now := d.m.clock.Now(); now.Before(d.retryAt) {
		d.setStatus(PathRelay, d.failReason, mode)
		return
	}
	if d.sel.Pinned(d.m.clock.Now()) && mode != selector.ModeDirect {
		d.setStatus(PathRelay, ReasonUnstable, mode)
		d.after(d.sel.PinnedUntil().Sub(d.m.clock.Now()), event{kind: evKick})
		return
	}
	d.connecting = true
	d.setStatus(PathConnectingDirect, "", mode)
	d.m.wg.Add(1)
	go func() {
		defer d.m.wg.Done()
		sess, reason := d.connect()
		if sess == nil {
			d.post(event{kind: evConnectFailed, reason: reason})
			return
		}
		select {
		case d.events <- event{kind: evConnected, sess: sess}:
		case <-d.done:
			sess.close()
		}
	}()
}

func (d *device) sample(sess *directSession, mode selector.Mode, ping, ok bool, rtt time.Duration) selector.Sample {
	sample := selector.Sample{LegUp: !sess.dead(), Ping: ping, PingOK: ok, PingRTT: rtt, Mode: mode}
	stats := sess.leg.Stats()
	if stats.PacketsSent > 0 {
		sample.HasLoss, sample.Loss = true, stats.Loss
	}
	d.mu.Lock()
	sample.RelayRTT = d.relayRTT
	d.loss, d.hasLoss = sample.Loss, sample.HasLoss
	if ping && ok {
		d.directRTT = rtt
	}
	d.mu.Unlock()
	return sample
}

func (d *device) connected(sess *directSession) {
	d.connecting = false
	mode := d.readMode()
	if reason := d.policyReason(mode); reason != "" {
		sess.close()
		d.setStatus(PathRelay, reason, mode)
		return
	}
	d.resetBackoff()
	d.failReason, d.networkChanged = "", false
	d.renewFails = 0
	d.sel.Reset()
	d.mu.Lock()
	d.session = sess
	d.localNAT, d.remoteNAT, d.stunOK = string(sess.localNAT), string(sess.remoteNAT), sess.stunOK
	d.mu.Unlock()
	d.m.wg.Add(1)
	go func() {
		defer d.m.wg.Done()
		select {
		case <-sess.leg.Done():
			d.post(event{kind: evLegDown, sess: sess})
		case <-sess.closed:
		case <-d.done:
		}
	}()
	d.scheduleGrant(sess)
	d.post(event{kind: evPingTick})
	d.post(event{kind: evRelayTick})
}

func (d *device) scheduleGrant(sess *directSession) {
	g := sess.currentGrant()
	now := d.m.clock.Now()
	renew := time.Duration(g.RenewAfterMS) * time.Millisecond
	if renew <= 0 {
		renew = time.Duration(g.ExpiresAt-g.IssuedAt) * time.Millisecond / 2
	}
	d.after(renew, event{kind: evRenewTick, sess: sess})
	d.after(max(time.UnixMilli(g.ExpiresAt).Sub(now)-ExpiryMargin, 0), event{kind: evExpiry, sess: sess, reason: ReasonConnectFailed})
}

func (d *device) legDown(sess *directSession) {
	if sess != d.currentSession() {
		return
	}
	reason := ReasonConnectFailed
	if text := closeReason(sess.closeReasonText()); text != "" {
		reason = text
	}
	d.lastDropped = sess
	d.dropSession(sess)
	d.failed(reason)
	d.setStatus(PathRelay, reason, d.readMode())
}

// closeReason maps an agent close frame's reason to a status reason; "" when it says nothing specific. After
// establishment "refused" only answers a re-attach the agent will not take (most often to a session it already
// ended) or ends a session the client replaced, so it says nothing about the server's policy.
func closeReason(text string) string {
	switch text {
	case signal.CloseRevoked:
		return ReasonServerDisabled
	}
	return ""
}

// dropSession forgets sess and closes it; streams on its leg resume on the relay through their loss hook.
func (d *device) dropSession(sess *directSession) {
	d.mu.Lock()
	if d.session == sess {
		d.session, d.direct = nil, false
	}
	d.mu.Unlock()
	for _, kind := range []eventKind{evPingTick, evRelayTick, evRenewTick, evExpiry} {
		d.cancelTimer(kind)
	}
	d.pingInFlight = false
	clear(d.pingSent)
	if d.leaving == sess {
		d.leaving = nil
	}
	d.sel.Leave(d.m.clock.Now())
	sess.close()
}

// leave moves every stream to the relay and closes the session; with makeBeforeBreak the streams migrate first.
func (d *device) leave(reason string, makeBeforeBreak bool) {
	sess := d.currentSession()
	d.mu.Lock()
	d.direct = false
	d.mu.Unlock()
	d.setStatus(PathRelay, reason, d.readMode())
	if sess == nil || sess == d.leaving {
		return
	}
	if !makeBeforeBreak {
		d.dropSession(sess)
		return
	}
	// No more pings or decisions on a leg being left; the migration's end closes it.
	d.leaving = sess
	d.cancelTimer(evPingTick)
	d.cancelTimer(evRelayTick)
	d.migrateAll(PathRelay, sess, true)
}

// closeSession drops the session at once.
func (d *device) closeSession(reason string) {
	if sess := d.currentSession(); sess != nil {
		d.mu.Lock()
		d.direct = false
		d.mu.Unlock()
		d.dropSession(sess)
	}
	if reason != "" {
		d.setStatus(PathRelay, reason, d.readMode())
	}
}

func (d *device) apply(action selector.Action, sess *directSession, mode selector.Mode) {
	switch action {
	case selector.UseDirect:
		d.mu.Lock()
		d.direct = true
		d.mu.Unlock()
		d.setStatus(PathDirect, "", mode)
		d.migrateAll(PathDirect, sess, false)
	case selector.UseRelay:
		d.mu.Lock()
		d.direct = false
		d.mu.Unlock()
		d.setStatus(PathRelay, d.sel.Reason(), mode)
		d.migrateAll(PathRelay, sess, false)
	case selector.CloseLeg:
		reason := d.sel.Reason()
		if mode == selector.ModeRelay {
			reason = ReasonModeRelay
		}
		d.leave(reason, true)
	default:
		d.mu.Lock()
		direct := d.direct
		d.mu.Unlock()
		current := d.status()
		switch {
		case direct:
			d.setStatus(PathDirect, "", mode)
		case current.Path == PathConnectingDirect:
			// A leg that has not won the streams yet: they stay on the relay.
			d.setStatus(PathRelay, d.sel.Reason(), mode)
		default:
			d.setStatus(current.Path, current.Reason, mode)
		}
	}
}

func (d *device) pingTick() {
	sess := d.currentSession()
	if sess == nil || d.pingInFlight {
		return
	}
	d.pingInFlight = true
	d.m.wg.Add(1)
	go func() {
		defer d.m.wg.Done()
		ctx, cancel := context.WithTimeout(d.ctx, selector.PingTimeout)
		rtt, err := sess.leg.Ping(ctx)
		cancel()
		d.post(event{kind: evPing, sess: sess, ok: err == nil, rtt: rtt})
	}()
}

func (d *device) pingResult(ev event) {
	if ev.sess != d.currentSession() {
		return
	}
	d.pingInFlight = false
	if ev.sess == d.leaving {
		return
	}
	// Only the cheap local checks run per ping; the proxy setting is checked when an attempt starts and on
	// every other evaluation.
	mode := d.readMode()
	if d.m.inBackground() {
		d.evaluate()
		return
	}
	d.apply(d.sel.Decide(d.m.clock.Now(), d.sample(ev.sess, mode, true, ev.ok, ev.rtt)), ev.sess, mode)
	if d.currentSession() != ev.sess {
		return
	}
	interval := selector.PingIntervalIdle
	d.mu.Lock()
	if d.direct && len(d.streams) > 0 {
		interval = selector.PingIntervalActive
	}
	d.mu.Unlock()
	d.after(interval, event{kind: evPingTick})
}

func (d *device) relayTick() {
	sess := d.currentSession()
	if sess == nil {
		return
	}
	d.pingSeq++
	seq := d.pingSeq
	d.pingSent[seq] = d.m.clock.Now()
	for old := range d.pingSent {
		if old+3 < seq {
			delete(d.pingSent, old)
		}
	}
	if sc := sess.signal(); sc != nil {
		go func() {
			ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
			defer cancel()
			_ = sc.Write(ctx, signal.Frame{Type: signal.TypePing, Seq: seq})
		}()
	}
	d.after(selector.RelayRTTInterval, event{kind: evRelayTick})
}

func (d *device) renew() {
	sess := d.currentSession()
	if sess == nil {
		return
	}
	d.m.wg.Add(1)
	go func() {
		defer d.m.wg.Done()
		ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
		defer cancel()
		g, err := d.m.backend.Grant(ctx, d.id, d.m.key.Public().(ed25519.PublicKey))
		if err != nil {
			d.post(event{kind: evRenewFailed, sess: sess, reason: grantReason(err)})
			return
		}
		d.post(event{kind: evRenewed, sess: sess, grant: g})
	}()
}

func (d *device) renewed(ev event) {
	if ev.sess != d.currentSession() {
		return
	}
	if ev.grant.GrantID != ev.sess.currentGrant().GrantID {
		// A new grant id is a new session on the server; this leg's grant will not be renewed.
		d.renewFailed(event{kind: evRenewFailed, sess: ev.sess, reason: ReasonConnectFailed})
		return
	}
	d.renewFails = 0
	ev.sess.setGrant(ev.grant)
	d.scheduleGrant(ev.sess)
	if sc := ev.sess.signal(); sc != nil {
		go func() {
			ctx, cancel := context.WithTimeout(d.ctx, 10*time.Second)
			defer cancel()
			_ = sc.Write(ctx, signal.Frame{Type: signal.TypeGrantRefresh, Grant: ev.grant.Grant})
		}()
	} else {
		go d.reattach(ev.sess)
	}
}

func (d *device) renewFailed(ev event) {
	if ev.sess != d.currentSession() {
		return
	}
	d.renewFails++
	d.m.logger.Info("direct grant renewal failed", "device_id", d.id, "reason", ev.reason, "attempt", d.renewFails)
	if timer := d.timers[evExpiry]; timer != nil {
		// Keep the expiry reason accurate.
		g := ev.sess.currentGrant()
		d.after(max(time.UnixMilli(g.ExpiresAt).Sub(d.m.clock.Now())-ExpiryMargin, 0), event{kind: evExpiry, sess: ev.sess, reason: ev.reason})
	}
	d.after(30*time.Second, event{kind: evRenewTick, sess: ev.sess})
}

// reattach replaces a dropped signal bridge of a live session.
func (d *device) reattach(sess *directSession) {
	ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
	defer cancel()
	conn, err := d.m.backend.DialSignal(ctx, d.id)
	if err != nil {
		return
	}
	sc := signal.NewConn(conn)
	g := sess.currentGrant()
	if err := sc.Write(ctx, signal.Frame{Type: signal.TypeAttach, Grant: g.Grant, ClientPub: clientPubText(d.m.key)}); err != nil {
		_ = sc.Close()
		return
	}
	go d.readSignal(sess, sc, nil, nil)
	d.post(event{kind: evSignalUp, sess: sess, sig: sc})
}

// migrateAll moves every live stream to target, terminals first, one at a time. A newer call supersedes it.
func (d *device) migrateAll(target string, sess *directSession, closeAfter bool) {
	gen := d.migrateGen.Add(1)
	d.mu.Lock()
	streams := make([]*liveStream, 0, len(d.streams))
	for _, ls := range d.streams {
		streams = append(streams, ls)
	}
	d.mu.Unlock()
	slices.SortStableFunc(streams, func(a, b *liveStream) int {
		ta, tb := a.purpose == rstream.PurposeTerminal, b.purpose == rstream.PurposeTerminal
		switch {
		case ta && !tb:
			return -1
		case tb && !ta:
			return 1
		}
		return 0
	})
	d.m.wg.Add(1)
	go func() {
		defer d.m.wg.Done()
		first := true
		for _, ls := range streams {
			if d.migrateGen.Load() != gen {
				return
			}
			select {
			case <-ls.stream.Done():
				continue
			default:
			}
			if ls.onPath() == target || target == PathDirect && !sess.allows(ls.purpose) {
				continue
			}
			if !first && !d.sleep(d.m.opts.MigrationSpacing) {
				return
			}
			first = false
			conn, err := d.carrier(d.ctx, target, sess, ls.purpose)
			if err != nil {
				if target == PathDirect {
					break
				}
				continue
			}
			ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
			err = ls.stream.Migrate(ctx, conn)
			cancel()
			if err == nil {
				ls.path.Store(target)
			}
		}
		d.post(event{kind: evMigrated, gen: gen, closeAfter: closeAfter, sess: sess})
	}()
}

func (d *device) sleep(delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-d.ctx.Done():
		return false
	}
}

// carrier opens a resume carrier on target: a stream of the session's leg, or a relay stream bridge.
func (d *device) carrier(ctx context.Context, target string, sess *directSession, purpose string) (net.Conn, error) {
	if target == PathDirect {
		if sess == nil || sess.dead() {
			return nil, errors.New("no direct leg")
		}
		return sess.openStream(ctx, purpose)
	}
	conn, stream, err := d.m.backend.DialRelay(ctx, d.id, purpose, true)
	if err != nil {
		return nil, err
	}
	if !stream {
		_ = conn.Close()
		return nil, rstream.Permanent(errors.New("server answered a resume with a raw bridge"))
	}
	return conn, nil
}

// redial is the stream's carrier-loss hook: the direct leg while the streams use it, the relay otherwise.
func (d *device) redial(ls *liveStream) func(ctx context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		d.mu.Lock()
		sess, direct := d.session, d.direct
		d.mu.Unlock()
		if direct && sess != nil && !sess.dead() {
			conn, err := sess.openStream(ctx, ls.purpose)
			if err == nil {
				ls.path.Store(PathDirect)
				return conn, nil
			}
			d.m.logger.Debug("direct resume carrier failed; using the relay", "device_id", d.id, "purpose", ls.purpose, "error", err)
		}
		conn, err := d.carrier(ctx, PathRelay, nil, ls.purpose)
		if err != nil {
			d.m.logger.Debug("relay resume carrier failed", "device_id", d.id, "purpose", ls.purpose, "error", err)
			return nil, err
		}
		ls.path.Store(PathRelay)
		return conn, nil
	}
}

// dial opens a new stream on the preferred path.
func (d *device) dial(ctx context.Context, purpose string) (net.Conn, error) {
	if purpose == "" {
		purpose = rstream.PurposeTerminal
	}
	d.mu.Lock()
	sess, direct := d.session, d.direct
	d.mu.Unlock()
	if direct && sess != nil && !sess.dead() {
		if conn, err := sess.openStream(ctx, purpose); err == nil {
			stream, err := rstream.Open(ctx, conn, purpose, d.m.opts.Stream)
			if err == nil {
				d.track(stream, purpose, PathDirect)
				return stream, nil
			}
		}
	}
	conn, stream, err := d.m.backend.DialRelay(ctx, d.id, purpose, false)
	if err != nil {
		return nil, err
	}
	if !stream {
		// The server or agent predates stream carriers, so this device cannot move streams to a direct leg.
		d.post(event{kind: evRaw})
		return conn, nil
	}
	resumable, err := rstream.Open(ctx, conn, purpose, d.m.opts.Stream)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeDisconnected, "无法打开远端设备桥: "+err.Error(), err)
	}
	d.track(resumable, purpose, PathRelay)
	d.post(event{kind: evKick})
	return resumable, nil
}

func (d *device) track(stream *rstream.Stream, purpose, path string) {
	ls := &liveStream{stream: stream, purpose: purpose}
	ls.path.Store(path)
	stream.ResumeWith(d.redial(ls))
	d.mu.Lock()
	d.streams[stream] = ls
	d.mu.Unlock()
	go func() {
		select {
		case <-stream.Done():
		case <-d.done:
			return
		}
		d.mu.Lock()
		delete(d.streams, stream)
		d.mu.Unlock()
		d.post(event{kind: evStreamGone})
	}()
}

// counts reports the live streams and how many use the direct leg.
func (d *device) counts() (total, direct int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, ls := range d.streams {
		total++
		if ls.onPath() == PathDirect {
			direct++
		}
	}
	return total, direct
}

// Counts reports the live streams of deviceID and how many run on the direct leg.
func (m *Manager) Counts(deviceID string) (total, direct int) {
	if d := m.existing(deviceID); d != nil {
		return d.counts()
	}
	return 0, 0
}
