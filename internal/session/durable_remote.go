package session

import (
	"context"
	"errors"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// durableProviderFor resolves the durable provider backing a durable tab on
// the given session. Local sessions use the manager's provider; SSH and device
// sessions resolve through the configured resolver and cache the result per
// session generation, so a reconnect re-resolves against the new transport.
// Automatic startup briefly caches helper failures; an explicit durable request retries immediately.
func (m *Manager) durableProviderFor(ctx context.Context, session *Session, transport *transportHandle, kind string, generation uint64, retryFailure bool) (base.DurableProvider, error) {
	if kind == KindLocal {
		if m.durable == nil {
			return nil, ErrUnsupported
		}
		return m.durable, nil
	}
	if (kind != KindSSH && kind != KindDevice) || m.resolver == nil || transport == nil {
		return nil, ErrUnsupported
	}
	session.durableMu.Lock()
	defer session.durableMu.Unlock()
	if session.durableProvider != nil && session.durableGen == generation && session.durableTransport == transport {
		return session.durableProvider, nil
	}
	if session.durableFailure != nil && session.durableGen == generation && session.durableTransport == transport &&
		!retryFailure && m.now().Sub(session.durableFailureAt) < 30*time.Second {
		return nil, session.durableFailure
	}
	session.durableFailure = nil
	session.durableFailureAt = time.Time{}
	assetID := session.Asset().ID
	resolveCtx := ipc.WithHelperProgressScope(ctx, ipc.HelperProgressScope{SessionID: session.ID, AssetID: assetID})
	provider, err := m.resolver.ResolveDurable(resolveCtx, transport.Transport)
	if err != nil {
		if kind == KindSSH && sshHelperUnavailable(err) && ctx.Err() == nil {
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
	if kind != KindSSH && kind != KindDevice {
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
	mode, err := m.terminalModeFor(ctx, session)
	if err != nil {
		return nil, err
	}
	if mode == TerminalModeRegular {
		return []base.DurableDescriptor{}, nil
	}

	provider, err := m.durableProviderFor(ctx, session, transport, kind, generation, false)
	if err != nil {
		return nil, err
	}
	lister, ok := provider.(base.DurableDescriptorLister)
	if !ok {
		return nil, ErrUnsupported
	}
	descriptors, err := lister.ListDurableDescriptors(ctx)
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
		if info.Kind != KindSSH && info.Kind != KindDevice {
			return TabInfo{}, ErrUnsupported
		}
		if info.Status != StatusConnected {
			return TabInfo{}, ErrDisconnected
		}
		mode, err := m.terminalModeFor(ctx, selected)
		if err != nil {
			return TabInfo{}, err
		}
		if mode == TerminalModeRegular {
			return TabInfo{}, ErrDurableDisabled
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
		canonical  bool
	}
	var matches []match
	for _, info := range infos {
		if (info.Kind != KindSSH && info.Kind != KindDevice) || info.Status != StatusConnected {
			continue
		}
		session, err := m.Session(info.ID)
		if err != nil {
			continue
		}
		mode, err := m.terminalModeFor(ctx, session)
		if err != nil {
			searchErrs = append(searchErrs, err)
			continue
		}
		if mode == TerminalModeRegular {
			continue
		}
		session.mu.Lock()
		transport := session.transport
		generation := session.generation
		session.mu.Unlock()
		provider, err := m.durableProviderFor(ctx, session, transport, info.Kind, generation, false)
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
		canonical, err := durableMetadataSupported(ctx, provider)
		if err != nil {
			searchErrs = append(searchErrs, err)
			continue
		}
		matches = append(matches, match{sessionID: session.ID, descriptor: descriptor, canonical: canonical})
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
		Recover: true, Title: selected.descriptor.Title, MetadataRevision: selected.descriptor.MetadataRevision, CanonicalMetadata: selected.canonical,
		RecoveredExited: selected.descriptor.Dead,
	}
	if selected.descriptor.Incarnation != "" {
		identity := base.DurableIdentity{CreatedAt: selected.descriptor.CreatedAt, Incarnation: selected.descriptor.Incarnation}
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
		if (info.Kind != KindSSH && info.Kind != KindDevice) || info.Status != StatusConnected {
			continue
		}
		mode, err := m.terminalModeFor(ctx, session)
		if err != nil {
			searchErrs = append(searchErrs, err)
			continue
		}
		if mode == TerminalModeRegular {
			continue
		}
		session.mu.Lock()
		transport := session.transport
		generation := session.generation
		session.mu.Unlock()
		provider, err := m.durableProviderFor(ctx, session, transport, info.Kind, generation, false)
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

func onlyHelperUnavailable(errs []error) bool {
	for _, err := range errs {
		if !sshHelperUnavailable(err) {
			return false
		}
	}
	return true
}

func findDurableDescriptor(ctx context.Context, provider base.DurableProvider, tabID string) (base.DurableDescriptor, bool, error) {
	if lister, ok := provider.(base.DurableDescriptorLister); ok {
		descriptors, err := lister.ListDurableDescriptors(ctx)
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
	lister, ok := provider.(DurableLister)
	if !ok {
		return base.DurableDescriptor{}, false, nil
	}
	ids, err := lister.ListDurable(ctx)
	if err != nil {
		return base.DurableDescriptor{}, false, err
	}
	for _, id := range ids {
		if id == tabID {
			return base.DurableDescriptor{ID: tabID}, true, nil
		}
	}
	return base.DurableDescriptor{}, false, nil
}

func durableMetadataSupported(ctx context.Context, provider base.DurableProvider) (bool, error) {
	metadata, ok := provider.(base.DurableMetadataProvider)
	if !ok {
		return false, nil
	}
	supported, err := metadata.DurableMetadataSupported(ctx)
	if errors.Is(err, base.ErrUnsupported) {
		return false, nil
	}
	return supported, err
}

func attachRecoveredDurable(ctx context.Context, provider base.DurableProvider, tabID string, identity *base.DurableIdentity) (base.DurableAttachment, error) {
	if identity == nil || identity.Incarnation == "" {
		return provider.Attach(ctx, tabID)
	}
	attacher, ok := provider.(base.DurableIdentityAttacher)
	if !ok {
		return nil, ErrUnsupported
	}
	return attacher.AttachWithIdentity(ctx, tabID, *identity)
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
	return base.DurableIdentity{CreatedAt: identity.CreatedAt, Incarnation: identity.Incarnation}, true
}

func (m *Manager) renameCanonicalTab(ctx context.Context, tab *Tab, provider base.DurableProvider, title string) (TabInfo, error) {
	attempt, cached, err := beginDurableRename(ctx, tab, provider, title)
	if err != nil {
		return TabInfo{}, err
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
	result, err := provider.(base.DurableMetadataProvider).RenameDurable(ctx, base.DurableRenameOptions{
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
		tab.durableIdentity = base.DurableIdentity{CreatedAt: descriptor.CreatedAt, Incarnation: descriptor.Incarnation}
		tab.title = descriptor.Title
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
			tab.title = conflict.CurrentTitle
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
				tab.durableIdentity = base.DurableIdentity{CreatedAt: descriptor.CreatedAt, Incarnation: descriptor.Incarnation}
				tab.title = descriptor.Title
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

// prefixSkipChannel drops the leading bytes of the wrapped channel so a
// reattached durable stream resumes where the previous attachment stopped
// instead of replaying consumed history into the terminal and transcript.
type prefixSkipChannel struct {
	base.Channel
	remaining int64
}

func (c *prefixSkipChannel) Read(p []byte) (int, error) {
	for c.remaining > 0 {
		discard := p
		if int64(len(discard)) > c.remaining {
			discard = discard[:c.remaining]
		}
		count, err := c.Channel.Read(discard)
		c.remaining -= int64(count)
		if err != nil {
			return 0, err
		}
		if count == 0 {
			return 0, nil
		}
	}
	return c.Channel.Read(p)
}
