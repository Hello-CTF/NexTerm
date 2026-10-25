package sync

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"github.com/Hello-CTF/NexTerm/internal/vault"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxPushBatchBytes = 32 << 20
	maxSyncAttempts   = 3
	maxPullPages      = 10000
	// Payload hashes are SHA-256 hex strings; this distinct value persists an undecryptable remote object's quarantine across sync rounds.
	quarantinedPayloadHash = "undecryptable"
)

// maxPullBytes is the client-side per-page pull budget; tests may lower it temporarily to construct pagination scenarios.
var maxPullBytes int64 = maxPullWireBytes

type SyncReport struct {
	Pulled        int      `json:"pulled"`
	Applied       int      `json:"applied"`
	PullSkipped   int      `json:"pullSkipped"`
	DecryptFailed int      `json:"decryptFailed"`
	Pushed        int      `json:"pushed"`
	Conflicts     int      `json:"conflicts"`
	Head          string   `json:"head"`
	Seq           int64    `json:"seq"`
	Warnings      []string `json:"warnings,omitempty"`

	unresolvedRefs map[string]bool
	// leftPending marks an object this round deliberately left unreconciled
	// (a credential while the vault is locked, or a missing dependency), so the
	// next round must list every server ID again instead of trusting the head.
	leftPending bool
}

// markUnresolvedRef marks an object applied in reference-degraded form (a dependency has not arrived): the engine must not record reconciliation,
// staying unreconciled so the next round re-pulls it and recovery happens automatically once the dependency arrives.
func (report *SyncReport) markUnresolvedRef(id string) {
	report.leftPending = true
	if report.unresolvedRefs == nil {
		report.unresolvedRefs = map[string]bool{}
	}
	report.unresolvedRefs[id] = true
}

// hasUnresolvedRef reports whether the object was applied in reference-degraded form this round; the mark lives for the whole syncOnce,
// letting the push side suppress pushing degraded versions back; it is cleared and recomputed at the start of each sync round.
func (report *SyncReport) hasUnresolvedRef(id string) bool {
	return report.unresolvedRefs[id]
}

type remoteSession struct {
	key            string
	userID         string
	client         *remoteClient
	dek            []byte
	envelopeHash   string
	credentialHash string
}

// persistedSession is the persisted account sync session envelope (setting sync.link.session, enc:v1:
// vault envelope, protected at the same level as the saved link password), letting the desktop process restore the session without login after a restart.
// Security posture change: the user DEK is persisted for the first time (previously memory-only); these fields never go into logs
// (aligned with the logSyncRound convention of logging counts only, never payloads).
type persistedSession struct {
	URL            string `json:"url"`
	Username       string `json:"username"`
	CookieToken    string `json:"cookieToken"`
	CSRFToken      string `json:"csrfToken"`
	UserID         string `json:"userId"`
	DEK            []byte `json:"dek"`
	EnvelopeHash   string `json:"envelopeHash,omitempty"`
	CredentialHash string `json:"credentialHash,omitempty"`
}

func linkCredentialHash(config RemoteConfig, dek []byte) string {
	mac := hmac.New(sha256.New, dek)
	for _, field := range []string{config.URL, config.Username, config.Password} {
		_, _ = mac.Write(binary.AppendUvarint(nil, uint64(len(field))))
		_, _ = mac.Write([]byte(field))
	}
	if config.Insecure {
		_, _ = mac.Write([]byte{1})
	} else {
		_, _ = mac.Write([]byte{0})
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func dekEnvelopeHash(envelopes *vault.UserDEKEnvelopes) string {
	buf := make([]byte, 0, len(envelopes.DEKEnvelope)+len(envelopes.KDFSalt)+len(envelopes.KDFParams)+24)
	for _, field := range [][]byte{envelopes.DEKEnvelope, envelopes.KDFSalt, []byte(envelopes.KDFParams)} {
		buf = binary.AppendUvarint(buf, uint64(len(field)))
		buf = append(buf, field...)
	}
	digest := sha256.Sum256(buf)
	return hex.EncodeToString(digest[:])
}

// verifySessionDEK prevents an old in-memory or persisted DEK from writing new cloud state
// after the account envelope changes. Unwrapping is needed only for a changed envelope.
func (e *Engine) verifySessionDEK(ctx context.Context, config RemoteConfig, session *remoteSession) error {
	envelopes, err := session.client.dekEnvelopes(ctx)
	if err != nil {
		return err
	}
	currentHash := dekEnvelopeHash(envelopes)
	if session.envelopeHash == currentHash {
		return nil
	}
	currentDEK, err := vault.UnwrapUserDEK(config.Password, envelopes.DEKEnvelope, envelopes.KDFSalt, envelopes.KDFParams)
	if err != nil {
		return err
	}
	matches := subtle.ConstantTimeCompare(currentDEK, session.dek) == 1
	clear(currentDEK)
	if !matches {
		return ipc.NewError(ipc.CodeForbidden, "账号数据密钥已变化，请重新连接同步账号")
	}
	session.envelopeHash = currentHash
	e.persistSession(ctx, config, session)
	return nil
}

type transcriptCacheKey struct {
	owner          string
	id             string
	sessionID      string
	assetID        string
	assetName      string
	assetKind      string
	startedAt      int64
	endedAt        int64
	hasEndedAt     bool
	bytes          int64
	chunks         int64
	truncated      bool
	contentOmitted bool
}

type transcriptCacheEntry struct {
	key         transcriptCacheKey
	payload     []byte
	payloadHash string
}

// Engine is the client engine of the v2 full-payload object protocol: the same local replica stays identical before and after login,
// login only attaches sync. The session and immutable transcript payloads are memory-cached; the local replica remains authoritative.
type Engine struct {
	store  *store.Store
	vault  *vault.Vault
	logger *slog.Logger

	opMu      sync.Mutex
	sessionMu sync.Mutex
	session   *remoteSession
	pending   map[string]*pendingLinkLogin

	transcriptCacheMu sync.RWMutex
	transcriptCache   map[string]transcriptCacheEntry
	aiShared          bool
}

func NewEngine(database *store.Store, credentialVault *vault.Vault, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{
		store: database, vault: credentialVault, logger: logger, pending: map[string]*pendingLinkLogin{},
		transcriptCache: map[string]transcriptCacheEntry{}, aiShared: true,
	}
}

// kindOptIn is the per-sync-round opt-in snapshot of sync object kinds; the zero value means hard-off.
type kindOptIn struct {
	knownHost    bool
	aiProfile    bool
	aiPermission bool
	aiGrant      bool
	preferences  bool
}

// loadKindOptIn reads the sync object opt-in for the session user; with no session identity (empty userID) it is hard-off, not falling back to global settings.
func (e *Engine) loadKindOptIn(ctx context.Context, userID string) (kindOptIn, error) {
	if userID == "" {
		return kindOptIn{}, nil
	}
	knownHost, err := e.store.KnownHostSyncOptIn(ctx, userID)
	if err != nil {
		return kindOptIn{}, err
	}
	aiProfile, err := e.store.AIProfileSyncOptIn(ctx, userID)
	if err != nil {
		return kindOptIn{}, err
	}
	aiPermission, err := e.store.AIPermissionSyncOptIn(ctx, userID)
	if err != nil {
		return kindOptIn{}, err
	}
	aiGrant, err := e.store.AIGrantSyncOptIn(ctx, userID)
	if err != nil {
		return kindOptIn{}, err
	}
	preferences, err := e.store.PreferencesSyncOptIn(ctx, userID)
	if err != nil {
		return kindOptIn{}, err
	}
	return kindOptIn{knownHost: knownHost, aiProfile: aiProfile, aiPermission: aiPermission, aiGrant: aiGrant, preferences: preferences}, nil
}

// key encodes the opt-in snapshot; a reconciled cursor is valid only for the snapshot it was reconciled under.
func (o kindOptIn) key() string {
	flags := []bool{o.knownHost, o.aiProfile, o.aiPermission, o.aiGrant, o.preferences}
	encoded := make([]byte, len(flags))
	for i, flag := range flags {
		encoded[i] = '0'
		if flag {
			encoded[i] = '1'
		}
	}
	return "optin:" + string(encoded)
}

// allowsRemote decides whether a remote object (including tombstones) may be applied this round; hard-off kinds are neither applied nor reconciled,
// staying unreconciled so a later round with opt-in enabled can pull and merge them.
func (o kindOptIn) allowsRemote(kind string, plaintext []byte) bool {
	switch kind {
	case KindKnownHost:
		return o.knownHost
	case KindAIProfile:
		return o.aiProfile
	case KindAIPermission:
		return o.aiPermission
	case KindAIGrant:
		return o.aiGrant
	case KindPreference:
		return o.preferences
	case KindTombstone:
		var payload tombstoneObject
		if err := json.Unmarshal(plaintext, &payload); err != nil {
			return true
		}
		switch payload.TargetKind {
		case KindKnownHost:
			return o.knownHost
		case KindAIProfile:
			return o.aiProfile
		case KindAIPermission:
			return o.aiPermission
		case KindAIGrant:
			return o.aiGrant
		case KindPreference:
			return o.preferences
		}
	}
	return true
}

// Sync runs one full sync round: reconcile -> pull and merge -> push local changes. Conflicts (another device pushed first or rolled back) retry automatically.
func (e *Engine) Sync(ctx context.Context, config RemoteConfig) (SyncReport, error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	report := SyncReport{}
	userID := ""
	for attempt := 0; attempt < maxSyncAttempts; attempt++ {
		session, err := e.ensureSession(ctx, config)
		if err != nil {
			e.logSyncRound(userID, report, err)
			return report, err
		}
		userID = session.userID
		if err := e.verifySessionDEK(ctx, config, session); err != nil {
			if errors.Is(err, errSessionExpired) {
				e.dropSession(session.key)
				continue
			}
			e.logSyncRound(userID, report, err)
			return report, err
		}
		conflict, err := e.syncOnce(ctx, session, &report)
		if errors.Is(err, errSessionExpired) {
			e.dropSession(session.key)
			continue
		}
		if err != nil {
			e.logSyncRound(userID, report, err)
			return report, err
		}
		if !conflict {
			e.logSyncRound(userID, report, nil)
			return report, nil
		}
		report.Conflicts++
		// Another device pushed at the same moment: back off a random moment so
		// both do not collide again on the retry.
		if err := sleepContext(ctx, conflictBackoff(attempt)); err != nil {
			return report, err
		}
	}
	err := ipc.NewError(ipc.CodeForbidden, "同步冲突次数过多，请稍后重试")
	e.logSyncRound(userID, report, err)
	return report, err
}

// logSyncRound logs at most one summary line per sync round: silent for empty rounds, Info for changes, Warn for failures;
// warnings log counts only; warning texts containing object IDs never go into logs.
func (e *Engine) logSyncRound(userID string, report SyncReport, syncErr error) {
	attrs := []any{
		"user", userID, "pulled", report.Pulled, "applied", report.Applied, "skipped", report.PullSkipped,
		"decrypt_failed", report.DecryptFailed, "pushed", report.Pushed, "conflicts", report.Conflicts,
		"warnings", len(report.Warnings),
	}
	if syncErr != nil {
		e.logger.Warn("sync round failed", append(attrs, "error", syncErr)...)
		return
	}
	if report.Pulled > 0 || report.Applied > 0 || report.PullSkipped > 0 ||
		report.DecryptFailed > 0 || report.Pushed > 0 || report.Conflicts > 0 {
		e.logger.Info("sync round complete", attrs...)
	}
	if len(report.Warnings) > 0 {
		e.logger.Warn("sync round warnings", "user", userID, "warnings", len(report.Warnings))
	}
}

func (e *Engine) ensureSession(ctx context.Context, config RemoteConfig) (*remoteSession, error) {
	key := config.URL + "\x00" + config.Username
	e.sessionMu.Lock()
	cached := e.session
	e.sessionMu.Unlock()
	if cached != nil && cached.key == key && subtle.ConstantTimeCompare(
		[]byte(cached.credentialHash), []byte(linkCredentialHash(config, cached.dek))) == 1 {
		return cached, nil
	}
	if cached != nil {
		e.dropSession(cached.key)
	}
	client, err := newRemoteClient(config)
	if err != nil {
		return nil, err
	}
	outcome, err := client.login(ctx, config.Username, config.Password)
	if err != nil {
		return nil, err
	}
	if outcome.mfaTicket != "" {
		return nil, errReloginMFA
	}
	if outcome.resetRequired {
		return nil, errReloginReset
	}
	return e.establishSession(ctx, config, client, config.Password)
}

// establishSession completes session onboarding after the password channel has passed: fetch the identity and DEK envelope, unwrap the DEK,
// cache the session, and persist it as a durable session.
func (e *Engine) establishSession(ctx context.Context, config RemoteConfig, client *remoteClient, password string) (*remoteSession, error) {
	userID, err := client.me(ctx)
	if err != nil {
		return nil, err
	}
	envelopes, err := client.dekEnvelopes(ctx)
	if err != nil {
		return nil, err
	}
	dek, err := vault.UnwrapUserDEK(password, envelopes.DEKEnvelope, envelopes.KDFSalt, envelopes.KDFParams)
	if err != nil {
		return nil, err
	}
	e.sessionMu.Lock()
	e.session = &remoteSession{key: remoteConfigKey(config), userID: userID, client: client, dek: dek,
		envelopeHash: dekEnvelopeHash(envelopes), credentialHash: linkCredentialHash(config, dek)}
	session := e.session
	e.sessionMu.Unlock()
	e.persistSession(ctx, config, session)
	return session, nil
}

// persistSession persists an established session as a durable session for login-free restore on the next process start;
// persist failures only log a warning and do not affect the established in-memory session.
func (e *Engine) persistSession(ctx context.Context, config RemoteConfig, session *remoteSession) {
	if e.vault == nil {
		return
	}
	encoded, err := json.Marshal(persistedSession{
		URL: config.URL, Username: config.Username,
		CookieToken: session.client.cookieToken, CSRFToken: session.client.csrfToken,
		UserID: session.userID, DEK: session.dek, EnvelopeHash: session.envelopeHash, CredentialHash: session.credentialHash,
	})
	if err != nil {
		e.logger.Warn("sync session persist failed", "error", err)
		return
	}
	protected, err := e.vault.EncryptSecret(ctx, string(encoded))
	if err != nil {
		e.logger.Warn("sync session persist failed", "error", err)
		return
	}
	if err := e.store.SettingSet(ctx, settingLinkSession, protected); err != nil {
		e.logger.Warn("sync session persist failed", "error", err)
	}
}

// loadPersistedSession reads the persisted session envelope: unsaved or vault locked/uninitialized is treated as no persisted session
// (locked stays a silent skip); a corrupt or undecryptable envelope is cleared with a warning and rewritten on next login.
func (e *Engine) loadPersistedSession(ctx context.Context) (*persistedSession, bool) {
	if e.vault == nil {
		return nil, false
	}
	value, found, err := e.store.SettingGet(ctx, settingLinkSession)
	if err != nil || !found || strings.TrimSpace(value) == "" {
		return nil, false
	}
	plaintext, err := e.vault.DecryptSecret(ctx, value)
	if err != nil {
		var appErr *ipc.Error
		if errors.As(err, &appErr) && (appErr.Code == ipc.CodeVaultLocked || appErr.Code == ipc.CodeVaultNotInit) {
			return nil, false
		}
		e.logger.Warn("persisted sync session undecryptable; clearing", "error", err)
		e.clearPersistedSession(ctx)
		return nil, false
	}
	var session persistedSession
	if err := json.Unmarshal([]byte(plaintext), &session); err != nil {
		e.logger.Warn("persisted sync session corrupted; clearing", "error", err)
		e.clearPersistedSession(ctx)
		return nil, false
	}
	if session.CookieToken == "" || session.UserID == "" || len(session.DEK) == 0 {
		e.clearPersistedSession(ctx)
		return nil, false
	}
	return &session, true
}

func (e *Engine) clearPersistedSession(ctx context.Context) {
	if err := e.store.SettingDelete(ctx, settingLinkSession); err != nil {
		e.logger.Warn("persisted sync session clear failed", "error", err)
	}
}

// restoreSession rebuilds the remote session from a persisted session: me() verifies the cookie is still valid and the identity matches the saved one,
// caching the session on success (no re-login, no DEK envelope unwrap); on failure the caller decides whether to clear it or fall back to password re-login.
func (e *Engine) restoreSession(ctx context.Context, config RemoteConfig, persisted *persistedSession) (*remoteSession, error) {
	if persisted.CredentialHash == "" || subtle.ConstantTimeCompare(
		[]byte(persisted.CredentialHash), []byte(linkCredentialHash(config, persisted.DEK))) != 1 {
		return nil, errSessionExpired
	}
	client, err := newRemoteClient(config)
	if err != nil {
		return nil, err
	}
	client.cookieToken = persisted.CookieToken
	client.csrfToken = persisted.CSRFToken
	userID, err := client.me(ctx)
	if err != nil {
		return nil, err
	}
	if userID != persisted.UserID {
		return nil, errSessionExpired
	}
	dek := make([]byte, len(persisted.DEK))
	copy(dek, persisted.DEK)
	e.sessionMu.Lock()
	e.session = &remoteSession{key: remoteConfigKey(config), userID: userID, client: client, dek: dek,
		envelopeHash: persisted.EnvelopeHash, credentialHash: persisted.CredentialHash}
	session := e.session
	e.sessionMu.Unlock()
	return session, nil
}

func (e *Engine) dropSession(key string) {
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	if e.session != nil && e.session.key == key {
		clear(e.session.dek)
		e.session = nil
	}
}

func (e *Engine) resetSession() {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	if e.session != nil {
		clear(e.session.dek)
		e.session = nil
	}
	e.pending = map[string]*pendingLinkLogin{}
}

// remoteWaitTarget returns the logged-in session's client and the head this
// device last saved, or ok=false without a session.
func (e *Engine) remoteWaitTarget(ctx context.Context) (client *remoteClient, cursorHead string, ok bool, err error) {
	e.sessionMu.Lock()
	session := e.session
	e.sessionMu.Unlock()
	if session == nil {
		return nil, "", false, nil
	}
	cursor, err := e.loadCursor(ctx, session.userID)
	if err != nil {
		return nil, "", false, err
	}
	return session.client, cursor.Head, true, nil
}

func (e *Engine) hasSession() bool {
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	return e.session != nil
}

// syncOnce runs a single sync round; conflict=true means the head diverged during push and reconciliation must rerun before retrying.
func (e *Engine) syncOnce(ctx context.Context, session *remoteSession, report *SyncReport) (bool, error) {
	userID := session.userID
	// Unresolved-ref marks are valid for this round only: objects applied degraded during the pull phase are neither reconciled nor re-pushed; conflict retries clear and recompute them.
	report.unresolvedRefs = nil
	optIn, err := e.loadKindOptIn(ctx, userID)
	if err != nil {
		return false, err
	}
	cursor, err := e.loadCursor(ctx, userID)
	if err != nil {
		return false, err
	}
	// A cursor saved by a fully reconciled round under the same opt-ins lets the
	// server answer "unchanged" instead of listing every object ID again.
	var knownHead []string
	if cursor.Head != "" && cursor.ReconciledFor == optIn.key() {
		knownHead = append(knownHead, cursor.Head)
	}
	cursor.ReconciledFor = ""
	idsResponse, err := session.client.ids(ctx, knownHead...)
	if err != nil {
		return false, err
	}
	syncStates, err := e.syncStateMap(ctx, userID)
	if err != nil {
		return false, err
	}
	server := make(map[string]IDEntry, len(idsResponse.Entries))
	unreconciled := make([]string, 0, len(idsResponse.Entries))
	if idsResponse.Unchanged && len(knownHead) > 0 {
		// The server is exactly as the last reconciled round left it, and that
		// round recorded every server object in sync_state.
		for id, state := range syncStates {
			server[id] = IDEntry{ID: id, BlobHash: state.blobHash}
		}
	} else {
		for _, entry := range idsResponse.Entries {
			server[entry.ID] = entry
			state, found := syncStates[entry.ID]
			if !found || state.blobHash != entry.BlobHash {
				unreconciled = append(unreconciled, entry.ID)
			}
		}
	}
	completed, err := e.pullAllAndApply(ctx, session, &cursor, unreconciled, report, optIn)
	if err != nil {
		return false, err
	}
	if !completed {
		// Reconciliation incomplete (stall): persist progress, but this round must not collect/push or continue with the newest head.
		if err := e.saveCursor(ctx, userID, cursor); err != nil {
			return false, err
		}
		return false, ipc.NewError(ipc.CodeDisconnected, "同步数据不完整（部分对象未返回），请稍后重试")
	}
	objects, err := e.collectLocalObjects(ctx, report, optIn, session.userID)
	if err != nil {
		return false, err
	}
	// Pull/apply updates reconciliation rows. Refresh them in one batch before deciding pushes;
	// no per-object sync_state queries are needed in either reconciliation loop.
	syncStates, err = e.syncStateMap(ctx, userID)
	if err != nil {
		return false, err
	}
	pending := make([]WireObject, 0, len(objects))
	pendingHashes := make([]string, 0, len(objects))
	for _, id := range sortObjectsForPush(objects) {
		if report.hasUnresolvedRef(id) {
			// Local is the reference-degraded form of the remote payload: do not push it back over the cloud object with references; normal reconciliation resumes once the dependency arrives.
			continue
		}
		local := objects[id]
		payloadHash := e.localPayloadHash(ctx, id, local)
		state, found := syncStates[id]
		statePayloadHash, stateBlobHash := state.payloadHash, state.blobHash
		serverEntry, onServer := server[id]
		if found && statePayloadHash == quarantinedPayloadHash && onServer && stateBlobHash == serverEntry.BlobHash {
			// A matching undecryptable cloud object needs an explicit repair decision. A changed blob is pulled and evaluated above.
			continue
		}
		if found && statePayloadHash == payloadHash && onServer && stateBlobHash == serverEntry.BlobHash {
			continue
		}
		blob, err := sealObjectWire(session.dek, local.plaintext, id, local.kind)
		if err != nil {
			return false, err
		}
		pending = append(pending, WireObject{ID: id, Blob: blob})
		pendingHashes = append(pendingHashes, payloadHash)
	}
	for len(pending) > 0 {
		batch, batchHashes, rest, restHashes := takePushBatch(pending, pendingHashes)
		response, err := session.client.push(ctx, cursor.Head, batch)
		if errors.Is(err, errHeadMismatch) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		for i, object := range batch {
			payloadHash := batchHashes[i]
			if err := e.syncStatePut(ctx, userID, object.ID, &payloadHash, hashBlob(object.Blob)); err != nil {
				return false, err
			}
		}
		report.Pushed += len(batch)
		cursor.Head = response.Head
		pending, pendingHashes = rest, restHashes
	}
	// Final pull: advance the cursor to the push point so idle sync stays silent and changes from other devices during the push are seen immediately.
	// The final pull has no catch-up IDs and completes naturally on an empty page; if it exceeds the bounded page count, log a warning without affecting the completed push.
	if completed, err := e.pullAllAndApply(ctx, session, &cursor, nil, report, optIn); err != nil {
		return false, err
	} else if !completed {
		report.warnf("收尾拉取超过页数上限，剩余对象将在下一轮同步")
		report.leftPending = true
	}
	if !report.leftPending {
		cursor.ReconciledFor = optIn.key()
	}
	if err := e.saveCursor(ctx, userID, cursor); err != nil {
		return false, err
	}
	report.Head = cursor.Head
	report.Seq = cursor.Seq
	return false, nil
}

// pullAllAndApply exhausts all cursor pages and confirms every unreconciled ID has been returned or no longer exists
// before push is allowed. Cursor progress advances only via next_seq (catch-up IDs must not advance the cursor or fake completion);
// the budget rule that the first object must be returned in full guarantees each page digests at least one object, so the loop always converges.
// Returning completed=false means stall (reconciliation incomplete); the caller must not collect/push this round.
func (e *Engine) pullAllAndApply(ctx context.Context, session *remoteSession, cursor *syncCursor, unreconciled []string, report *SyncReport, optIn kindOptIn) (bool, error) {
	remaining := make(map[string]bool, len(unreconciled))
	for _, id := range unreconciled {
		remaining[id] = true
	}
	stalls := 0
	for page := 0; page < maxPullPages; page++ {
		lookup := make([]string, 0, len(remaining))
		for id := range remaining {
			lookup = append(lookup, id)
		}
		if len(lookup) > maxPullIDLookup {
			lookup = lookup[:maxPullIDLookup]
		}
		pullResponse, err := session.client.pull(ctx, cursor.Seq, lookup, maxPullBytes)
		if err != nil {
			return false, err
		}
		returned := 0
		for _, object := range pullResponse.Objects {
			if err := e.applyRemoteObject(ctx, session.userID, object, session.dek, report, optIn); err != nil {
				return false, err
			}
			delete(remaining, object.ID)
			returned++
		}
		// The cursor advances only via next_seq (cursor objects); catch-up IDs do not affect the cursor position or completion decision.
		advanced := pullResponse.NextSeq != cursor.Seq
		cursor.Seq = pullResponse.NextSeq
		cursor.Head = pullResponse.Head
		if advanced && returned > 0 {
			// Checkpoint every applied page so an interrupted large pull resumes here.
			// The checkpoint is never marked reconciled; the round end decides that.
			checkpoint := *cursor
			checkpoint.ReconciledFor = ""
			if err := e.saveCursor(ctx, session.userID, checkpoint); err != nil {
				return false, err
			}
		}
		if pullResponse.CursorDone && len(remaining) == 0 {
			return true, nil
		}
		if returned == 0 && len(remaining) > 0 && pullResponse.CursorDone {
			stalls++
			if stalls >= 3 {
				e.logger.Warn("sync pull stalled on missing objects; deferring to next round", "remaining", len(remaining))
				return false, nil
			}
		} else {
			stalls = 0
		}
	}
	e.logger.Warn("sync pull exceeded page bound; deferring to next round", "remaining", len(remaining))
	return false, nil
}

// conflictBackoff returns a randomized delay that grows with each head conflict.
func conflictBackoff(attempt int) time.Duration {
	base := time.Duration(100*(attempt+1)) * time.Millisecond
	return base + time.Duration(rand.Int64N(int64(base)))
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func takePushBatch(objects []WireObject, hashes []string) (batch []WireObject, batchHashes []string, rest []WireObject, restHashes []string) {
	batch = []WireObject{}
	batchHashes = []string{}
	size := 0
	for i, object := range objects {
		if i > 0 && int64(size+len(object.Blob)) > maxPushBatchBytes {
			return batch, batchHashes, objects[i:], hashes[i:]
		}
		batch = append(batch, object)
		batchHashes = append(batchHashes, hashes[i])
		size += len(object.Blob)
	}
	return batch, batchHashes, nil, nil
}

type syncCursor struct {
	Seq  int64  `json:"seq"`
	Head string `json:"head"`
	// ReconciledFor is the opt-in snapshot of the round that saved this cursor
	// with every server object reconciled; empty means the next round must list
	// all server IDs.
	ReconciledFor string `json:"reconciledFor,omitempty"`
}

func cursorSettingKey(userID string) string {
	return "sync.cursor." + userID
}

func (e *Engine) loadCursor(ctx context.Context, userID string) (syncCursor, error) {
	cursor := syncCursor{}
	raw, found, err := e.store.SettingGet(ctx, cursorSettingKey(userID))
	if err != nil {
		return cursor, err
	}
	if !found || raw == "" {
		return cursor, nil
	}
	if err := json.Unmarshal([]byte(raw), &cursor); err != nil {
		e.logger.Warn("sync cursor corrupted; resetting", "error", err)
		return syncCursor{}, nil
	}
	return cursor, nil
}

func (e *Engine) saveCursor(ctx context.Context, userID string, cursor syncCursor) error {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "无法编码同步游标", err)
	}
	return e.store.SettingSet(ctx, cursorSettingKey(userID), string(encoded))
}

type syncStateSnapshot struct {
	payloadHash string
	blobHash    string
}

func (e *Engine) syncStateMap(ctx context.Context, userID string) (map[string]syncStateSnapshot, error) {
	state := query.Use(e.store.GORM()).SyncState
	rows, err := state.WithContext(ctx).Where(state.UserID.Eq(userID)).Find()
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	result := make(map[string]syncStateSnapshot, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		payloadHash := ""
		if row.PayloadHash != nil {
			payloadHash = string(row.PayloadHash)
		}
		result[row.ObjectID] = syncStateSnapshot{payloadHash: payloadHash, blobHash: hex.EncodeToString(row.BlobHash)}
	}
	return result, nil
}

func (e *Engine) syncStateGet(ctx context.Context, userID, objectID string) (payloadHash string, blobHash string, found bool, returnErr error) {
	state := query.Use(e.store.GORM()).SyncState
	row, err := state.WithContext(ctx).Where(state.UserID.Eq(userID), state.ObjectID.Eq(objectID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if row.PayloadHash != nil {
		payloadHash = string(row.PayloadHash)
	}
	return payloadHash, hex.EncodeToString(row.BlobHash), true, nil
}

func (e *Engine) syncStatePut(ctx context.Context, userID, objectID string, payloadHash *string, blobHash [32]byte) error {
	var payload []byte
	if payloadHash != nil {
		payload = []byte(*payloadHash)
	}
	if err := e.store.GORM().WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "object_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"payload_hash", "blob_hash"}),
	}).Create(&store.SyncState{UserID: userID, ObjectID: objectID, PayloadHash: payload, BlobHash: blobHash[:]}).Error; err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

type syncTombstoneRow struct {
	ID        string
	Kind      string
	DeletedAt int64
}

// syncTombstoneList can exclude AI rows before querying when an identity-less server caller may only use non-AI sync state.
func (e *Engine) syncTombstoneList(ctx context.Context, includeAI ...bool) ([]syncTombstoneRow, error) {
	tombstone := query.Use(e.store.GORM()).SyncTombstone
	result := []syncTombstoneRow{}
	statement := tombstone.WithContext(ctx).Select(tombstone.ID, tombstone.Kind, tombstone.DeletedAt)
	if len(includeAI) > 0 && !includeAI[0] {
		statement = statement.Where(tombstone.Kind.NotIn(KindAIProfile, KindAIPermission, KindAIGrant))
	}
	err := statement.
		Where(ownerConditions(ctx, tombstone.TableName())...).
		Order(tombstone.ID).
		Scan(&result)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return result, nil
}

func (e *Engine) syncTombstoneGet(ctx context.Context, objectID string) (syncTombstoneRow, bool, error) {
	tombstone := query.Use(e.store.GORM()).SyncTombstone
	row, err := tombstone.WithContext(ctx).
		Where(tombstone.ID.Eq(objectID)).
		Where(ownerConditions(ctx, tombstone.TableName())...).
		First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return syncTombstoneRow{}, false, nil
	}
	if err != nil {
		return syncTombstoneRow{}, false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return syncTombstoneRow{ID: row.ID, Kind: row.Kind, DeletedAt: row.DeletedAt}, true, nil
}

func (e *Engine) syncTombstonePut(ctx context.Context, objectID, kind string, deletedAt int64) error {
	if err := e.store.GORM().WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "owner_id"}, {Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"deleted_at": store.MaxExpression(e.store.GORM(), "sync_tombstone", "deleted_at", nil),
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "owner_id"}, Value: ipc.OwnerID(ctx)}}},
	}).Create(&store.SyncTombstone{ID: objectID, Kind: kind, DeletedAt: deletedAt, OwnerID: ipc.OwnerID(ctx)}).Error; err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

// snippetUpsert mirrors store.GroupUpsert semantics: idempotent write by ID preserving the peer revision,
// clearing the same-ID deletion tombstone in the same transaction: once a newer snippet beats an old tombstone, no tombstone that could roll back the object may remain.
// The write binds to the snapshot the LWW decision was made on (nil for a snippet that did not exist locally).
func (e *Engine) snippetUpsert(ctx context.Context, row store.SnippetRow, snapshot *store.SnippetRow) error {
	return e.store.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return e.snippetUpsertTxBound(ctx, tx, row, snapshot)
	})
}

// snippetUpsertTxBound writes one snippet inside a transaction with the write bound to
// the caller's snapshot: the planner's equal-revision winner lands while a concurrent
// local write is never overwritten (the conflict leaves the object unreconciled for the
// next round). The explicit bundle import passes the pre-import snapshot (or the earlier
// in-bundle entry's content for duplicate IDs).
func (e *Engine) snippetUpsertTxBound(ctx context.Context, tx *gorm.DB, row store.SnippetRow, snapshot *store.SnippetRow) error {
	row.OwnerID = ipc.OwnerID(ctx)
	if snapshot == nil {
		if err := store.UpsertOwnerGuardedExpected(ctx, tx, &row, []string{"group_id", "name", "body", "sort", "updated_at"}, 0); err != nil {
			return err
		}
	} else {
		if _, err := store.UpsertOwnerGuardedExpectedContent(ctx, tx, &row, []string{"group_id", "name", "body", "sort", "updated_at"}, snapshot); err != nil {
			return err
		}
	}
	tombstone := query.Use(tx).SyncTombstone
	if _, err := tombstone.WithContext(ctx).
		Where(tombstone.ID.Eq(row.ID)).
		Where(ownerConditions(ctx, tombstone.TableName())...).
		Delete(); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

func isNotFound(err error) bool {
	var appErr *ipc.Error
	return errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound
}
