package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

var errHelperDeploymentPending = fmt.Errorf("%w: helper deployment is still in progress", durable.ErrUnavailable)

type backgroundHelperDeployment struct {
	session        *Session
	transport      *transportHandle
	generation     uint64
	sequence       uint64
	operationID    string
	canceledByUser bool
	done           chan struct{}
	sessionCtx     context.Context
	ctx            context.Context
	cancel         context.CancelFunc
	stop           func() bool
	provider       base.DurableProvider
	err            error
}

var backgroundHelperDeployments = struct {
	sync.Mutex
	entries map[*Session]*backgroundHelperDeployment
}{entries: make(map[*Session]*backgroundHelperDeployment)}

var completedHelperDeployments = struct {
	sync.Mutex
	entries map[*Session]*backgroundHelperDeployment
}{entries: make(map[*Session]*backgroundHelperDeployment)}

var helperDeploymentSequence uint64

type helperOperationReporterKey struct{}

func withHelperOperationReporter(ctx context.Context, entry *backgroundHelperDeployment) context.Context {
	return context.WithValue(ctx, helperOperationReporterKey{}, func(operationID string) {
		if operationID == "" {
			return
		}
		backgroundHelperDeployments.Lock()
		defer backgroundHelperDeployments.Unlock()
		if entry.operationID == "" {
			entry.operationID = operationID
		}
	})
}

// ReportHelperOperation binds resolver progress to the real session deployment.
// The first operation ID wins, so a late report cannot rename an active retry.
func ReportHelperOperation(ctx context.Context, operationID string) {
	if report, ok := ctx.Value(helperOperationReporterKey{}).(func(string)); ok {
		report(operationID)
	}
}

func matchingBackgroundHelper(session *Session, transport *transportHandle, generation uint64) *backgroundHelperDeployment {
	backgroundHelperDeployments.Lock()
	defer backgroundHelperDeployments.Unlock()
	entry := backgroundHelperDeployments.entries[session]
	if entry == nil || entry.transport != transport || entry.generation != generation {
		return nil
	}
	return entry
}

func cancelStaleBackgroundHelper(session *Session, transport *transportHandle, generation uint64) {
	backgroundHelperDeployments.Lock()
	entry := backgroundHelperDeployments.entries[session]
	if entry != nil && (entry.transport != transport || entry.generation != generation) {
		delete(backgroundHelperDeployments.entries, session)
		entry.cancel()
	}
	backgroundHelperDeployments.Unlock()
	completedHelperDeployments.Lock()
	completed := completedHelperDeployments.entries[session]
	if completed != nil && (completed.transport != transport || completed.generation != generation) {
		delete(completedHelperDeployments.entries, session)
	}
	completedHelperDeployments.Unlock()
}

func (m *Manager) automaticDurableProvider(ctx context.Context, session *Session, transport *transportHandle, generation uint64) (base.DurableProvider, error) {
	if m.resolver == nil || transport == nil {
		return nil, ErrUnsupported
	}
	session.durableMu.Lock()
	if session.durableProvider != nil && session.durableGen == generation && session.durableTransport == transport {
		provider := session.durableProvider
		session.durableMu.Unlock()
		return provider, nil
	}
	if session.durableFailure != nil && session.durableGen == generation && session.durableTransport == transport && m.now().Sub(session.durableFailureAt) < durableFailureRetryWindow {
		err := session.durableFailure
		session.durableMu.Unlock()
		return nil, err
	}
	session.durableMu.Unlock()

	entry, started, err := m.ensureBackgroundHelper(session, transport, generation, true)
	if err != nil {
		return nil, err
	}
	if !started {
		return nil, errHelperDeploymentPending
	}
	// Only an already-fast resolver can satisfy the current tab. This wait never
	// cancels or fails the deployment; slower helpers continue in background.
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-entry.done:
		m.logger.Debug("helper deployment finished within the first-tab wait", "session", session.ID, "ok", entry.err == nil)
		session.mu.Lock()
		current := session.transport == transport && session.generation == generation
		session.mu.Unlock()
		if !current {
			return nil, ErrStaleGeneration
		}
		if entry.err == nil && entry.provider != nil {
			return entry.provider, nil
		}
		return nil, entry.err
	case <-timer.C:
		m.logger.Debug("helper deployment still running after the first-tab wait; tab will use a regular PTY", "session", session.ID, "waited_ms", 50)
		return nil, errHelperDeploymentPending
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

// ensureBackgroundHelper returns the helper deployment for this session
// generation, starting one when none is running. started reports whether this
// call launched it. It never waits for the deployment and holds no durableMu. A session that is still
// reconnecting may deploy (requireConnected false); only the first-tab path insists on a connected session.
func (m *Manager) ensureBackgroundHelper(session *Session, transport *transportHandle, generation uint64, requireConnected bool) (entry *backgroundHelperDeployment, started bool, err error) {
	session.mu.Lock()
	if session.closed || requireConnected && session.status != StatusConnected {
		session.mu.Unlock()
		return nil, false, ErrDisconnected
	}
	if session.transport != transport || session.generation != generation {
		session.mu.Unlock()
		return nil, false, ErrStaleGeneration
	}
	backgroundHelperDeployments.Lock()
	if entry := backgroundHelperDeployments.entries[session]; entry != nil {
		if entry.transport == transport && entry.generation == generation {
			backgroundHelperDeployments.Unlock()
			session.mu.Unlock()
			return entry, false, nil
		}
		delete(backgroundHelperDeployments.entries, session)
		entry.cancel()
	}
	backgroundHelperDeployments.Unlock()
	// A deployment that just finished for this generation already stored its provider under session.mu; a caller that
	// read the provider before that must join the finished deployment instead of starting a second one.
	session.durableMu.Lock()
	storedProvider := session.durableProvider
	if session.durableGen != generation || session.durableTransport != transport {
		storedProvider = nil
	}
	session.durableMu.Unlock()
	completedHelperDeployments.Lock()
	if completed := completedHelperDeployments.entries[session]; completed != nil && completed.transport == transport &&
		completed.generation == generation && completed.err == nil && completed.provider != nil && completed.provider == storedProvider {
		completedHelperDeployments.Unlock()
		session.mu.Unlock()
		return completed, false, nil
	}
	completedHelperDeployments.Unlock()
	// Entries are only added under session.mu, which is still held, so the map has no entry for this session now.
	backgroundHelperDeployments.Lock()
	baseCtx := session.ctx
	if baseCtx == nil {
		baseCtx = m.ctx
	}
	deploymentCtx, cancel := context.WithCancel(baseCtx)
	entry = &backgroundHelperDeployment{
		session: session, transport: transport, generation: generation, done: make(chan struct{}), sessionCtx: baseCtx, ctx: deploymentCtx, cancel: cancel,
	}
	helperDeploymentSequence++
	entry.sequence = helperDeploymentSequence
	entry.stop = context.AfterFunc(m.ctx, cancel)
	context.AfterFunc(baseCtx, func() {
		completedHelperDeployments.Lock()
		defer completedHelperDeployments.Unlock()
		if completedHelperDeployments.entries[session] == entry {
			delete(completedHelperDeployments.entries, session)
		}
	})
	backgroundHelperDeployments.entries[session] = entry
	backgroundHelperDeployments.Unlock()
	completedHelperDeployments.Lock()
	delete(completedHelperDeployments.entries, session)
	completedHelperDeployments.Unlock()
	session.mu.Unlock()
	if !m.startGoroutine(func() { m.runBackgroundHelper(entry) }) {
		backgroundHelperDeployments.Lock()
		if backgroundHelperDeployments.entries[session] == entry {
			delete(backgroundHelperDeployments.entries, session)
		}
		backgroundHelperDeployments.Unlock()
		entry.stop()
		cancel()
		return nil, false, fmt.Errorf("%w: manager is closing", durable.ErrUnavailable)
	}
	return entry, true, nil
}

func isPureCancellation(err error) bool {
	if err == nil {
		return false
	}
	if unwrap, ok := err.(interface{ Unwrap() []error }); ok {
		children := unwrap.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !isPureCancellation(child) {
				return false
			}
		}
		return true
	}
	if unwrap, ok := err.(interface{ Unwrap() error }); ok {
		return isPureCancellation(unwrap.Unwrap())
	}
	return err == context.Canceled
}

func (m *Manager) runBackgroundHelper(entry *backgroundHelperDeployment) {
	defer entry.cancel()
	defer entry.stop()
	session := entry.session
	resolveCtx := ipc.WithHelperProgressScope(entry.ctx, ipc.HelperProgressScope{SessionID: session.ID, AssetID: session.Asset().ID})
	resolveCtx = withHelperOperationReporter(resolveCtx, entry)
	deployStart := time.Now()
	m.logger.Debug("background helper deployment started", "session", session.ID, "generation", entry.generation, "sequence", entry.sequence)
	provider, err := m.resolver.ResolveDurable(resolveCtx, entry.transport.Transport)
	finish := []any{"session", session.ID, "generation", entry.generation, "sequence", entry.sequence,
		"ok", err == nil && provider != nil, "elapsed_ms", time.Since(deployStart).Milliseconds()}
	if err != nil {
		finish = append(finish, "error", err.Error())
	}
	m.logger.Debug("background helper deployment finished", finish...)
	entry.provider, entry.err = provider, err
	if entry.err == nil && entry.provider == nil {
		entry.err = errors.New("helper resolver returned a nil provider")
	}
	// The outcome is stored and the running entry removed under session.mu, the lock ensureBackgroundHelper holds:
	// a caller that no longer sees the running deployment therefore already sees its provider or failure, and never
	// starts a second deployment in between.
	session.mu.Lock()
	backgroundHelperDeployments.Lock()
	canceledByUser := entry.canceledByUser
	if backgroundHelperDeployments.entries[session] == entry {
		delete(backgroundHelperDeployments.entries, session)
	}
	backgroundHelperDeployments.Unlock()
	recordCompletedHelperDeployment(entry)
	if session.transport == entry.transport && session.generation == entry.generation {
		session.durableMu.Lock()
		session.durableGen = entry.generation
		session.durableTransport = entry.transport
		if entry.err == nil {
			session.durableProvider = entry.provider
			session.durableFailure = nil
			session.durableFailureAt = time.Time{}
			session.durableCreateFailure = nil
		} else if canceledByUser && isPureCancellation(entry.err) {
			session.durableProvider = nil
			session.durableFailure = nil
			session.durableFailureAt = time.Time{}
		} else {
			session.durableProvider = nil
			session.durableFailure = entry.err
			session.durableFailureAt = m.now()
		}
		session.durableMu.Unlock()
	}
	session.mu.Unlock()
	close(entry.done)
}

func recordCompletedHelperDeployment(entry *backgroundHelperDeployment) {
	if entry.sessionCtx != nil && entry.sessionCtx.Err() != nil {
		return
	}
	completedHelperDeployments.Lock()
	defer completedHelperDeployments.Unlock()
	if completed := completedHelperDeployments.entries[entry.session]; completed == nil || completed.sequence < entry.sequence {
		completedHelperDeployments.entries[entry.session] = entry
	}
}

func (m *Manager) helperDeploymentTarget(ctx context.Context, sessionID string) (*Session, *transportHandle, uint64, error) {
	connected, err := m.SessionFor(ctx, sessionID)
	if err != nil {
		return nil, nil, 0, err
	}
	connected.mu.Lock()
	defer connected.mu.Unlock()
	if connected.asset.Kind != KindSSH {
		return nil, nil, 0, ErrUnsupported
	}
	if connected.closed || connected.status != StatusConnected || connected.transport == nil {
		return nil, nil, 0, ErrDisconnected
	}
	return connected, connected.transport, connected.generation, nil
}

// CancelHelperOperation cancels only the matching live deployment. It does not
// disconnect SSH, close a tab, or kill any durable process on the target.
func (m *Manager) CancelHelperOperation(ctx context.Context, sessionID, operationID string) error {
	if operationID == "" {
		return ErrInvalidOptions
	}
	connected, transport, generation, err := m.helperDeploymentTarget(ctx, sessionID)
	if err != nil {
		return err
	}
	backgroundHelperDeployments.Lock()
	defer backgroundHelperDeployments.Unlock()
	entry := backgroundHelperDeployments.entries[connected]
	if entry == nil || entry.transport != transport || entry.generation != generation || entry.operationID != operationID {
		return ErrStaleGeneration
	}
	entry.canceledByUser = true
	entry.cancel()
	return nil
}

// RetryHelperOperation starts a fresh resolver budget after a completed failed
// operation. The old operation and session generation must still be current.
func (m *Manager) RetryHelperOperation(ctx context.Context, sessionID, operationID string) error {
	if operationID == "" {
		return ErrInvalidOptions
	}
	connected, transport, generation, err := m.helperDeploymentTarget(ctx, sessionID)
	if err != nil {
		return err
	}
	backgroundHelperDeployments.Lock()
	active := backgroundHelperDeployments.entries[connected]
	backgroundHelperDeployments.Unlock()
	if active != nil {
		return ErrInvalidOptions
	}
	completedHelperDeployments.Lock()
	completed := completedHelperDeployments.entries[connected]
	if completed == nil || completed.transport != transport || completed.generation != generation || completed.operationID != operationID {
		completedHelperDeployments.Unlock()
		return ErrStaleGeneration
	}
	select {
	case <-completed.done:
	default:
		completedHelperDeployments.Unlock()
		return ErrInvalidOptions
	}
	failed := completed.err != nil
	completedHelperDeployments.Unlock()
	if !failed {
		return ErrInvalidOptions
	}
	connected.durableMu.Lock()
	if connected.durableGen == generation && connected.durableTransport == transport {
		connected.durableProvider = nil
		connected.durableFailure = nil
		connected.durableFailureAt = time.Time{}
		connected.durableCreateFailure = nil
	}
	connected.durableMu.Unlock()
	_, err = m.automaticDurableProvider(ctx, connected, transport, generation)
	if errors.Is(err, errHelperDeploymentPending) {
		return nil
	}
	return err
}

// durableProviderFor resolves and caches a provider per session generation.
// Explicit requests can wait for an in-flight background deployment; automatic
// terminal creation uses automaticDurableProvider to return ordinary SSH first.
func (m *Manager) durableProviderFor(ctx context.Context, session *Session, transport *transportHandle, kind string, generation uint64, retryFailure bool) (base.DurableProvider, error) {
	if kind == KindLocal {
		// retryFailure marks the explicit paths (list, recover, kill) that
		// work on existing terminals; new tabs pass false.
		if retryFailure {
			return m.localDurableProvider(ctx, localRecoverWait, true)
		}
		return m.localDurableProvider(ctx, localOpenWait, false)
	}
	if (kind != KindSSH && kind != KindDevice) || m.resolver == nil || transport == nil {
		return nil, ErrUnsupported
	}
	session.durableMu.Lock()
	if session.durableProvider != nil && session.durableGen == generation && session.durableTransport == transport {
		provider := session.durableProvider
		session.durableMu.Unlock()
		return provider, nil
	}
	entry := matchingBackgroundHelper(session, transport, generation)
	if entry == nil && kind == KindSSH {
		cancelStaleBackgroundHelper(session, transport, generation)
	}
	if entry != nil {
		session.durableMu.Unlock()
		select {
		case <-entry.done:
			if entry.err == nil && entry.provider != nil {
				session.mu.Lock()
				current := session.transport == transport && session.generation == generation
				session.mu.Unlock()
				if !current {
					return nil, ErrStaleGeneration
				}
				return entry.provider, nil
			}
			if !retryFailure {
				if entry.err == nil {
					return nil, errors.New("helper resolver returned a nil provider")
				}
				return nil, entry.err
			}
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
		session.durableMu.Lock()
		if session.durableProvider != nil && session.durableGen == generation && session.durableTransport == transport {
			provider := session.durableProvider
			session.durableMu.Unlock()
			return provider, nil
		}
	}
	if kind == KindSSH {
		// Deploying while holding durableMu would stall every first tab of the session behind this call (the tab
		// open path and fallback notice need the same lock), so no SSH deployment may run under it.
		if session.durableFailure != nil && session.durableGen == generation && session.durableTransport == transport &&
			!retryFailure && m.now().Sub(session.durableFailureAt) < durableFailureRetryWindow {
			err := session.durableFailure
			session.durableMu.Unlock()
			return nil, err
		}
		session.durableMu.Unlock()
		session.mu.Lock()
		current := session.transport == transport && session.generation == generation
		session.mu.Unlock()
		if current {
			// The session's own transport shares the single background deployment; callers only wait.
			return m.awaitSSHHelper(ctx, session, transport, generation, retryFailure)
		}
		// A reconnect candidate is not installed yet, so it resolves on its own, still outside the lock.
		return m.resolveSSHCandidate(ctx, session, transport, generation)
	}
	defer session.durableMu.Unlock()
	if session.durableFailure != nil && session.durableGen == generation && session.durableTransport == transport &&
		!retryFailure && m.now().Sub(session.durableFailureAt) < durableFailureRetryWindow {
		return nil, session.durableFailure
	}
	session.durableFailure = nil
	session.durableFailureAt = time.Time{}
	resolveCtx := ipc.WithHelperProgressScope(ctx, ipc.HelperProgressScope{SessionID: session.ID, AssetID: session.Asset().ID})
	provider, err := m.resolver.ResolveDurable(resolveCtx, transport.Transport)
	if err != nil {
		return nil, err
	}
	session.durableProvider = provider
	session.durableFailure = nil
	session.durableFailureAt = time.Time{}
	session.durableCreateFailure = nil
	session.durableGen = generation
	session.durableTransport = transport
	return provider, nil
}

// resolveSSHCandidate resolves the helper for a transport that is not yet the session's current one (a reconnect in
// progress) without holding durableMu while the helper is resolved.
func (m *Manager) resolveSSHCandidate(ctx context.Context, session *Session, transport *transportHandle, generation uint64) (base.DurableProvider, error) {
	// Candidates are serialized per session so two reconnect attempts never deploy to the same host at once.
	session.helperCandidateMu.Lock()
	defer session.helperCandidateMu.Unlock()
	session.durableMu.Lock()
	session.durableFailure = nil
	session.durableFailureAt = time.Time{}
	session.durableMu.Unlock()
	resolveCtx := ipc.WithHelperProgressScope(ctx, ipc.HelperProgressScope{SessionID: session.ID, AssetID: session.Asset().ID})
	provider, err := m.resolver.ResolveDurable(resolveCtx, transport.Transport)
	session.durableMu.Lock()
	defer session.durableMu.Unlock()
	if err != nil {
		if sshHelperUnavailable(err) && ctx.Err() == nil {
			session.durableProvider = nil
			session.durableFailure = err
			session.durableFailureAt = m.now()
			session.durableGen = generation
			session.durableTransport = transport
		}
		return nil, err
	}
	session.durableProvider = provider
	session.durableFailure = nil
	session.durableFailureAt = time.Time{}
	session.durableCreateFailure = nil
	session.durableGen = generation
	session.durableTransport = transport
	return provider, nil
}

// awaitSSHHelper waits for the background helper deployment of this session generation, starting it when
// none is running. With retryFailure a failed deployment is retried once from scratch.
func (m *Manager) awaitSSHHelper(ctx context.Context, session *Session, transport *transportHandle, generation uint64, retryFailure bool) (base.DurableProvider, error) {
	for attempt := 0; ; attempt++ {
		entry, started, err := m.ensureBackgroundHelper(session, transport, generation, false)
		if err != nil {
			return nil, err
		}
		select {
		case <-entry.done:
			if entry.err == nil && entry.provider != nil {
				session.mu.Lock()
				current := session.transport == transport && session.generation == generation
				session.mu.Unlock()
				if !current {
					return nil, ErrStaleGeneration
				}
				return entry.provider, nil
			}
			// Retry only when this call joined a deployment someone else started and it failed; a deployment this
			// call started itself already is the fresh attempt.
			if !retryFailure || started || attempt > 0 {
				if entry.err == nil {
					return nil, errors.New("helper resolver returned a nil provider")
				}
				return nil, entry.err
			}
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}

func sshHelperUnavailable(err error) bool {
	return errors.Is(err, ErrUnsupported) || errors.Is(err, base.ErrUnsupported) || errors.Is(err, durable.ErrUnavailable)
}

func (s *Session) cacheDurableCreateFailure(transport *transportHandle, generation uint64, err error) {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if s.durableProvider == nil || s.durableGen != generation || s.durableTransport != transport {
		return
	}
	s.durableCreateFailure = err
}

func (s *Session) cachedDurableCreateFailure(transport *transportHandle, generation uint64) error {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if s.durableGen != generation || s.durableTransport != transport {
		return nil
	}
	return s.durableCreateFailure
}

func (s *Session) clearDurableCreateFailure(transport *transportHandle, generation uint64) {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if s.durableGen == generation && s.durableTransport == transport {
		s.durableCreateFailure = nil
	}
}

func (s *Session) claimDurableFallbackNotice(generation uint64) bool {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if s.fallbackNotified && s.fallbackNoticeGen == generation {
		return false
	}
	s.fallbackNotified = true
	s.fallbackNoticeGen = generation
	return true
}

// ListRemoteDurable lists durable descriptors on one explicitly selected,
// connected SSH or managed device session. It never searches another session or creates a tab.
func (m *Manager) ListRemoteDurable(ctx context.Context, sessionID string) ([]base.DurableDescriptor, error) {
	session, err := m.Session(sessionID)
	if err != nil {
		return nil, err
	}
	session.mu.Lock()
	kind := session.asset.Kind
	if kind != KindSSH && kind != KindDevice && (kind != KindLocal || !m.localDurableEnabled()) {
		session.mu.Unlock()
		return nil, ErrUnsupported
	}
	if !sessionOwnerAllowed(session, userIDFromContext(ctx)) {
		session.mu.Unlock()
		return nil, ErrForbidden
	}
	if session.status != StatusConnected || session.transport == nil {
		session.mu.Unlock()
		return nil, ErrDisconnected
	}
	transport := session.transport
	generation := session.generation
	session.mu.Unlock()
	if _, err := m.terminalModeFor(ctx, session); err != nil {
		return nil, err
	}

	// Listing runs on a poll. A recorded deployment failure must stay in force for this connection, otherwise
	// every poll after the failure cache expires would redeploy against a host where the helper cannot run.
	if kind == KindSSH {
		session.durableMu.Lock()
		failure := session.durableFailure
		// Only "this host cannot run the helper" is sticky; transient failures keep the normal retry window.
		known := failure != nil && sshHelperUnavailable(failure) && session.durableProvider == nil && session.durableGen == generation && session.durableTransport == transport
		session.durableMu.Unlock()
		if known {
			return nil, failure
		}
	}
	provider, err := m.durableProviderFor(ctx, session, transport, kind, generation, kind == KindLocal)
	if localHelperNotRunning(kind, err) {
		// No local helper runs, so no local terminal of an earlier run is alive.
		return []base.DurableDescriptor{}, nil
	}
	if err != nil {
		return nil, err
	}
	descriptors, err := provider.ListDurableDescriptors(ctx)
	if err != nil {
		return nil, err
	}
	if descriptors == nil {
		descriptors = []base.DurableDescriptor{}
	}
	return descriptors, nil
}

// RecoverRemoteDurable searches connected SSH and device sessions for a tab ID.
// When options.SessionID is set, only that session is searched.
// It returns ErrTabNotFound only when the search completes with no owning daemon;
// resolver and enumeration failures are joined when no owner is found, or if
// an error other than helper unavailability prevents a reliable search.
func (m *Manager) RecoverRemoteDurable(ctx context.Context, tabID string, options OpenTabOptions) (TabInfo, error) {
	return m.RecoverRemoteDurableInSessions(ctx, tabID, options, nil)
}

// RecoverRemoteDurableInSessions restricts discovery to session IDs already
// authorized by the caller. A nil list searches all manager sessions.
func (m *Manager) RecoverRemoteDurableInSessions(ctx context.Context, tabID string, options OpenTabOptions, sessionIDs []string) (TabInfo, error) {
	infos := m.ListSessions()
	if options.SessionID != "" {
		if sessionIDs != nil {
			allowed := false
			for _, id := range sessionIDs {
				if id == options.SessionID {
					allowed = true
					break
				}
			}
			if !allowed {
				return TabInfo{}, ErrForbidden
			}
		}
		selected, err := m.Session(options.SessionID)
		if err != nil {
			return TabInfo{}, err
		}
		info := selected.Info()
		if !m.durableHostKind(info.Kind) {
			return TabInfo{}, ErrUnsupported
		}
		if info.Status != StatusConnected {
			return TabInfo{}, ErrDisconnected
		}
		if _, err := m.terminalModeFor(ctx, selected); err != nil {
			return TabInfo{}, err
		}
		infos = []SessionInfo{info}
	} else if sessionIDs != nil {
		allowed := make(map[string]bool, len(sessionIDs))
		for _, id := range sessionIDs {
			allowed[id] = true
		}
		filtered := make([]SessionInfo, 0, len(sessionIDs))
		for _, info := range infos {
			if allowed[info.ID] {
				filtered = append(filtered, info)
			}
		}
		infos = filtered
	}

	var searchErrs []error
	type match struct {
		sessionID  string
		descriptor base.DurableDescriptor
	}
	var matches []match
	for _, info := range infos {
		if !m.durableHostKind(info.Kind) || info.Status != StatusConnected {
			continue
		}
		session, err := m.Session(info.ID)
		if err != nil {
			continue
		}
		if _, err := m.terminalModeFor(ctx, session); err != nil {
			searchErrs = append(searchErrs, err)
			continue
		}
		session.mu.Lock()
		transport := session.transport
		generation := session.generation
		session.mu.Unlock()
		provider, err := m.durableProviderFor(ctx, session, transport, info.Kind, generation, info.Kind == KindLocal)
		if localHelperNotRunning(info.Kind, err) {
			continue
		}
		if err != nil {
			searchErrs = append(searchErrs, err)
			continue
		}
		descriptor, found, err := findDurableDescriptor(ctx, provider, tabID)
		if err != nil {
			searchErrs = append(searchErrs, err)
			continue
		}
		if !found {
			continue
		}
		matches = append(matches, match{sessionID: session.ID, descriptor: descriptor})
	}
	if len(matches) > 1 {
		return TabInfo{}, ErrAmbiguousDurable
	}
	if len(matches) == 1 && !onlyHelperUnavailable(searchErrs) {
		return TabInfo{}, errors.Join(searchErrs...)
	}
	if len(matches) == 0 {
		if len(searchErrs) > 0 {
			return TabInfo{}, errors.Join(searchErrs...)
		}
		return TabInfo{}, ErrTabNotFound
	}
	selected := matches[0]
	options.TabID = tabID
	options.SessionID = selected.sessionID
	options.Durable = &DurableTabOptions{
		Recover: true, Title: selected.descriptor.Title, MetadataRevision: selected.descriptor.MetadataRevision,
		RecoveredExited: selected.descriptor.Dead,
	}
	if selected.descriptor.Incarnation != "" {
		identity := base.DurableIdentity{CreatedAt: selected.descriptor.CreatedAt, Incarnation: selected.descriptor.Incarnation, Generation: selected.descriptor.Generation}
		options.Durable.Identity = &identity
	}
	return m.OpenTab(ctx, options)
}

// RemoteDurableOwners enumerates the selected connected hosts without attaching.
// The caller must supply session IDs authorized for the requesting user.
func (m *Manager) RemoteDurableOwners(ctx context.Context, tabID string, sessionIDs []string) ([]string, error) {
	owners := make([]string, 0)
	var searchErrs []error
	for _, id := range sessionIDs {
		session, err := m.SessionFor(ctx, id)
		if err != nil {
			searchErrs = append(searchErrs, err)
			continue
		}
		info := session.Info()
		if !m.durableHostKind(info.Kind) || info.Status != StatusConnected {
			continue
		}
		if _, err := m.terminalModeFor(ctx, session); err != nil {
			searchErrs = append(searchErrs, err)
			continue
		}
		session.mu.Lock()
		transport := session.transport
		generation := session.generation
		session.mu.Unlock()
		provider, err := m.durableProviderFor(ctx, session, transport, info.Kind, generation, info.Kind == KindLocal)
		if localHelperNotRunning(info.Kind, err) {
			continue
		}
		if err != nil {
			searchErrs = append(searchErrs, err)
			continue
		}
		_, found, err := findDurableDescriptor(ctx, provider, tabID)
		if err != nil {
			searchErrs = append(searchErrs, err)
			continue
		}
		if found {
			owners = append(owners, id)
		}
	}
	if len(searchErrs) > 0 && (len(owners) == 0 || !onlyHelperUnavailable(searchErrs)) {
		return nil, errors.Join(searchErrs...)
	}
	return owners, nil
}

// durableHostKind reports a session kind whose durable terminals live in a
// helper this manager can list and recover from.
func (m *Manager) durableHostKind(kind string) bool {
	return kind == KindSSH || kind == KindDevice || kind == KindLocal && m.localDurableEnabled()
}

func onlyHelperUnavailable(errs []error) bool {
	for _, err := range errs {
		if !sshHelperUnavailable(err) {
			return false
		}
	}
	return true
}

func findDurableDescriptor(ctx context.Context, provider base.DurableProvider, tabID string) (base.DurableDescriptor, bool, error) {
	descriptors, err := provider.ListDurableDescriptors(ctx)
	if err != nil {
		return base.DurableDescriptor{}, false, err
	}
	for _, descriptor := range descriptors {
		if descriptor.ID == tabID {
			return descriptor, true, nil
		}
	}
	return base.DurableDescriptor{}, false, nil
}

// attachRecoveredDurable opens a durable tab this process has no resume
// offset for, replaying only the recent tail; retain, when set, also streams
// the output after that offset for the transcript.
func attachRecoveredDurable(ctx context.Context, provider base.DurableProvider, tabID string, identity *base.DurableIdentity, retain *uint64) (base.DurableAttachment, error) {
	if identity == nil || identity.Incarnation == "" {
		return nil, ErrDurableUnrecoverable
	}
	return provider.AttachTailWithIdentity(ctx, tabID, *identity, base.DurableTailOptions{Tail: base.DurableFreshTailBytes, Retain: retain})
}

func durableReplayOf(attachment base.DurableAttachment) (base.DurableReplay, bool) {
	reporter, ok := attachment.(base.DurableReplayReporter)
	if !ok {
		return base.DurableReplay{}, false
	}
	return reporter.DurableReplay(), true
}

func durableAttachmentIdentity(attachment base.DurableAttachment) (base.DurableIdentity, bool) {
	source, ok := attachment.(interface {
		Identity() supervisor.Identity
	})
	if !ok {
		return base.DurableIdentity{}, false
	}
	identity := source.Identity()
	if identity.Incarnation == "" {
		return base.DurableIdentity{}, false
	}
	return base.DurableIdentity{CreatedAt: identity.CreatedAt, Incarnation: identity.Incarnation, Generation: identity.Generation}, true
}

func (m *Manager) renameCanonicalTab(ctx context.Context, tab *Tab, provider base.DurableProvider, title string, at int64) (TabInfo, error) {
	attempt, cached, err := beginDurableRename(ctx, tab, provider, title)
	if err != nil {
		return TabInfo{}, err
	}
	if !cached {
		attempt.changedAt = at
	}
	if cached {
		if attempt.err != nil {
			return TabInfo{}, attempt.err
		}
		if err := validateDurableResult(ctx, provider, tab.ID, base.DurableIdentity{Incarnation: attempt.incarnation}, base.DurableRenameResult{Title: attempt.resultTitle, MetadataRevision: attempt.resultMetadataRevision}); err != nil {
			return m.finishDurableRename(ctx, tab, provider, attempt, base.DurableRenameResult{}, err)
		}
		tab.mu.Lock()
		info := tab.infoLocked()
		tab.mu.Unlock()
		return info, nil
	}
	tab.mu.Lock()
	identity := tab.durableIdentity
	metadataRevision := attempt.expectedMetadataRevision
	tab.mu.Unlock()
	if identity.Incarnation == "" {
		return TabInfo{}, durable.ErrIdentity
	}
	result, err := provider.RenameDurable(ctx, base.DurableRenameOptions{
		ID: tab.ID, Incarnation: identity.Incarnation, RequestID: attempt.requestID, ExpectedMetadataRevision: metadataRevision, Title: title,
	})
	return m.finishDurableRename(ctx, tab, provider, attempt, result, err)
}

func beginDurableRename(ctx context.Context, tab *Tab, provider base.DurableProvider, title string) (*durableRenameAttempt, bool, error) {
	tab.mu.Lock()
	defer tab.mu.Unlock()
	if tab.closed || tab.destroying {
		return nil, false, ErrTabClosed
	}
	descriptor, found, err := findDurableDescriptor(ctx, provider, tab.ID)
	if err != nil {
		return nil, false, err
	}
	if found && descriptor.Incarnation != tab.durableIdentity.Incarnation {
		tab.durableIdentity = base.DurableIdentity{CreatedAt: descriptor.CreatedAt, Incarnation: descriptor.Incarnation, Generation: descriptor.Generation}
		tab.adoptHelperTitleLocked(descriptor.Title)
		tab.metadataRevision = descriptor.MetadataRevision
	}
	for id, attempt := range tab.renameAttempts {
		if attempt.completed && attempt.err != nil {
			delete(tab.renameAttempts, id)
			continue
		}
		if attempt.completed && attempt.err == nil && attempt.title == title && attempt.resultTitle == tab.title && attempt.resultMetadataRevision == tab.metadataRevision {
			return attempt, true, nil
		}
		if attempt.title == title && attempt.expectedMetadataRevision == tab.metadataRevision && attempt.incarnation == tab.durableIdentity.Incarnation {
			return attempt, false, nil
		}
	}
	if tab.renameAttempts == nil {
		tab.renameAttempts = make(map[string]*durableRenameAttempt)
	}
	if len(tab.renameAttempts) >= 16 {
		for id, attempt := range tab.renameAttempts {
			if attempt.completed {
				delete(tab.renameAttempts, id)
			}
		}
	}
	attempt := &durableRenameAttempt{requestID: ids.New(), incarnation: tab.durableIdentity.Incarnation, expectedMetadataRevision: tab.metadataRevision, title: title}
	tab.renameAttempts[attempt.requestID] = attempt
	return attempt, false, nil
}

// adoptHelperTitleLocked takes the canonical title the shared helper reports
// (another instance may have renamed or cleared it). The helper carries no
// decision time, so a title that differs from the current one is stamped
// with this backend's clock: that decision is at least as new as now from
// this backend's view, and a stale client decision must not win over it.
func (t *Tab) adoptHelperTitleLocked(title string) {
	custom := title != ""
	if title != t.title || custom != t.titleCustom {
		t.titleChangedAt = time.Now().UnixMilli()
	}
	t.title = title
	t.titleCustom = custom
}

func (m *Manager) finishDurableRename(ctx context.Context, tab *Tab, provider base.DurableProvider, attempt *durableRenameAttempt, result base.DurableRenameResult, err error) (TabInfo, error) {
	attempt.err = err
	if err == nil {
		if validationErr := validateDurableResult(ctx, provider, tab.ID, base.DurableIdentity{Incarnation: attempt.incarnation}, result); validationErr != nil {
			err = validationErr
			attempt.err = validationErr
			attempt.completed = !unknownRenameError(err)
		} else {
			tab.mu.Lock()
			if tab.closed || tab.destroying {
				tab.mu.Unlock()
				return TabInfo{}, ErrTabClosed
			}
			tab.title = result.Title
			tab.titleCustom = result.Title != ""
			if attempt.changedAt > 0 {
				tab.titleChangedAt = attempt.changedAt
			}
			tab.metadataRevision = result.MetadataRevision
			attempt.resultTitle = result.Title
			attempt.resultMetadataRevision = result.MetadataRevision
			attempt.completed = true
			event := tab.controlEventLocked()
			info := tab.infoLocked()
			tab.mu.Unlock()
			m.emit(ctx, TopicTerminalControl, event)
			return info, nil
		}
	}

	attempt.err = err
	attempt.completed = !unknownRenameError(err)
	if !unknownRenameError(err) {
		delete(tab.renameAttempts, attempt.requestID)
	}
	var conflict *base.DurableMetadataConflictError
	if errors.As(err, &conflict) {
		attempt.resultTitle = conflict.CurrentTitle
		attempt.resultMetadataRevision = conflict.CurrentMetadataRevision
		tab.mu.Lock()
		if !tab.closed && !tab.destroying {
			tab.adoptHelperTitleLocked(conflict.CurrentTitle)
			tab.metadataRevision = conflict.CurrentMetadataRevision
			event := tab.controlEventLocked()
			tab.mu.Unlock()
			m.emit(ctx, TopicTerminalControl, event)
		} else {
			tab.mu.Unlock()
		}
		return TabInfo{}, err
	}
	if errors.Is(err, durable.ErrIdentity) {
		descriptor, found, findErr := findDurableDescriptor(ctx, provider, tab.ID)
		if findErr != nil {
			return TabInfo{}, errors.Join(err, findErr)
		}
		if found {
			attempt.resultTitle = descriptor.Title
			attempt.resultMetadataRevision = descriptor.MetadataRevision
			tab.mu.Lock()
			if !tab.closed && !tab.destroying {
				tab.durableIdentity = base.DurableIdentity{CreatedAt: descriptor.CreatedAt, Incarnation: descriptor.Incarnation, Generation: descriptor.Generation}
				tab.adoptHelperTitleLocked(descriptor.Title)
				tab.metadataRevision = descriptor.MetadataRevision
				event := tab.controlEventLocked()
				tab.mu.Unlock()
				m.emit(ctx, TopicTerminalControl, event)
			} else {
				tab.mu.Unlock()
			}
		}
	}
	if attempt.resultTitle == "" {
		attempt.resultTitle = tab.title
		attempt.resultMetadataRevision = tab.metadataRevision
	}
	return TabInfo{}, err
}

func validateDurableResult(ctx context.Context, provider base.DurableProvider, tabID string, identity base.DurableIdentity, result base.DurableRenameResult) error {
	descriptor, found, err := findDurableDescriptor(ctx, provider, tabID)
	if err != nil {
		return err
	}
	if !found {
		return durable.ErrNotFound
	}
	if descriptor.Incarnation != identity.Incarnation {
		return durable.ErrIdentity
	}
	if descriptor.Title != result.Title || descriptor.MetadataRevision != result.MetadataRevision {
		return &base.DurableMetadataConflictError{CurrentTitle: descriptor.Title, CurrentMetadataRevision: descriptor.MetadataRevision}
	}
	return nil
}

func unknownRenameError(err error) bool {
	if errors.Is(err, base.ErrUnsupported) {
		return false
	}
	return errors.Is(err, durable.ErrUnavailable) || errors.Is(err, base.ErrDisconnected) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (t *Tab) addDurableFed(count int64) {
	t.mu.Lock()
	if t.durable != nil {
		t.durableFed += count
	}
	t.mu.Unlock()
}
