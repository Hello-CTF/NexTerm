package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const (
	mfaTicketBytes = 32
	mfaTicketTTL   = 5 * time.Minute
	// mfaMaxTicketsPerUser bounds pending second-factor tickets per user; issuing
	// one more revokes the oldest.
	mfaMaxTicketsPerUser = 3
	// mfaLockThreshold consecutive wrong second-factor codes lock the user's
	// second factor for mfaLockDuration, regardless of ticket or client IP.
	// Reaching the threshold again after a lock expired doubles the next lock
	// (30m, 1h, 2h, ...) up to mfaLockMaxDuration. A success clears the
	// escalation; so does mfaLockDecay without any lock after the last one ended.
	mfaLockThreshold   = 10
	mfaLockDuration    = 15 * time.Minute
	mfaLockMaxDuration = 24 * time.Hour
	mfaLockDecay       = 24 * time.Hour

	mfaLockedMessage = "第二因素验证失败次数过多，已暂时锁定，请稍后再试"
)

type mfaTicket struct {
	userID    string
	deviceID  string
	expiresAt time.Time
	// epoch is the account generation at ticket issue; the confirm re-checks
	// it so a revocation or reset between the two steps fails the login closed.
	epoch uint64
	// loginKey is the password-step throttle key, cleared only once the second factor passes.
	loginKey string
	seq      uint64
}

// mfaTicketStore holds one-time tickets for "password passed, second factor pending" (memory only).
// A restart invalidates them and the user simply re-enters the password; tickets are random 256-bit values, not enumerable.
type mfaTicketStore struct {
	mu      sync.Mutex
	entries map[string]*mfaTicket
	now     func() time.Time
	seq     uint64
}

func newMFATicketStore() *mfaTicketStore {
	return &mfaTicketStore{entries: make(map[string]*mfaTicket), now: time.Now}
}

func (s *mfaTicketStore) issue(userID, deviceID, loginKey string, epoch uint64) (string, time.Time) {
	raw := make([]byte, mfaTicketBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}
	}
	ticket := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) > 4096 {
		s.sweepLocked(now)
	}
	s.revokeOldestLocked(userID, now)
	s.seq++
	s.entries[ticket] = &mfaTicket{userID: userID, deviceID: deviceID, expiresAt: now.Add(mfaTicketTTL), epoch: epoch, loginKey: loginKey, seq: s.seq}
	return ticket, now.Add(mfaTicketTTL)
}

// revokeOldestLocked makes room for one more ticket of the user.
func (s *mfaTicketStore) revokeOldestLocked(userID string, now time.Time) {
	for {
		count, oldest, oldestSeq := 0, "", uint64(0)
		for ticket, entry := range s.entries {
			if entry.userID != userID || now.After(entry.expiresAt) {
				continue
			}
			count++
			if oldest == "" || entry.seq < oldestSeq {
				oldest, oldestSeq = ticket, entry.seq
			}
		}
		if count < mfaMaxTicketsPerUser {
			return
		}
		delete(s.entries, oldest)
	}
}

func (s *mfaTicketStore) get(ticket string) (*mfaTicket, bool) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[ticket]
	if !ok || now.After(entry.expiresAt) {
		delete(s.entries, ticket)
		return nil, false
	}
	copied := *entry
	return &copied, true
}

func (s *mfaTicketStore) delete(ticket string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, ticket)
}

func (s *mfaTicketStore) sweepLocked(now time.Time) {
	for ticket, entry := range s.entries {
		if now.After(entry.expiresAt) {
			delete(s.entries, ticket)
		}
	}
}

type mfaLockEntry struct {
	fails       int
	inflight    int
	lockedUntil time.Time
	// level counts the locks since the last success; it sets the next duration.
	level int
	// lastLockEnd is when the most recent lock expired (zero while none has).
	lastLockEnd time.Time
}

// mfaLockDurationFor is the lock length for the given 1-based escalation level.
func mfaLockDurationFor(level int) time.Duration {
	d := mfaLockDuration
	for i := 1; i < level; i++ {
		if d >= mfaLockMaxDuration/2 {
			return mfaLockMaxDuration
		}
		d *= 2
	}
	if d > mfaLockMaxDuration {
		return mfaLockMaxDuration
	}
	return d
}

// mfaLockout counts second-factor failures per user. Unlike the LRU-backed
// LoginThrottle it never evicts a live entry, so flooding other keys cannot
// clear a lock. Entries exist only for users who passed the password step.
type mfaLockout struct {
	mu      sync.Mutex
	entries map[string]*mfaLockEntry
	now     func() time.Time
}

func newMFALockout() *mfaLockout {
	return &mfaLockout{entries: make(map[string]*mfaLockEntry), now: time.Now}
}

// begin reserves one verification attempt. Counting in-flight attempts keeps
// a concurrent burst from verifying more than mfaLockThreshold codes.
func (l *mfaLockout) begin(userID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.entries) > 1024 {
		for id, e := range l.entries {
			if e.inflight == 0 && !e.lockedUntil.IsZero() && !now.Before(e.lockedUntil) && now.Sub(e.lockedUntil) >= mfaLockDecay {
				delete(l.entries, id)
			}
		}
	}
	e := l.entries[userID]
	if e == nil {
		e = &mfaLockEntry{}
		l.entries[userID] = e
	}
	if !e.lockedUntil.IsZero() {
		if now.Before(e.lockedUntil) {
			return false
		}
		e.fails, e.lastLockEnd, e.lockedUntil = 0, e.lockedUntil, time.Time{}
	}
	if e.level > 0 && e.fails == 0 && e.inflight == 0 && !e.lastLockEnd.IsZero() && now.Sub(e.lastLockEnd) >= mfaLockDecay {
		e.level = 0
	}
	if e.fails+e.inflight >= mfaLockThreshold {
		return false
	}
	e.inflight++
	return true
}

// release ends a reservation without counting it (verification error).
func (l *mfaLockout) release(userID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.entries[userID]; e != nil && e.inflight > 0 {
		e.inflight--
	}
}

func (l *mfaLockout) success(userID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.entries[userID]; e != nil {
		if e.inflight > 1 {
			e.fails, e.level, e.lockedUntil, e.lastLockEnd = 0, 0, time.Time{}, time.Time{}
			e.inflight--
		} else {
			delete(l.entries, userID)
		}
	}
}

// clear forgets all lock state of a user after a credential reset and reports
// whether a failure count or lock was actually dropped. An entry with attempts
// in flight is kept (zeroed) so their later failure/success/release stay
// balanced; their outcome belongs to the new generation and can at most
// count one failure, never revive the old lock or escalation level.
func (l *mfaLockout) clear(userID string) (cleared bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[userID]
	if e == nil {
		return false
	}
	cleared = e.fails > 0 || e.level > 0 || !e.lockedUntil.IsZero() || !e.lastLockEnd.IsZero()
	if e.inflight > 0 {
		e.fails, e.level, e.lockedUntil, e.lastLockEnd = 0, 0, time.Time{}, time.Time{}
	} else {
		delete(l.entries, userID)
	}
	return cleared
}

// failure counts a wrong code; locked is true only for the call that crosses the threshold.
func (l *mfaLockout) failure(userID string) (fails int, until time.Time, level int, locked bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[userID]
	if e == nil {
		return 0, time.Time{}, 0, false
	}
	if e.inflight > 0 {
		e.inflight--
	}
	e.fails++
	if e.fails >= mfaLockThreshold && e.lockedUntil.IsZero() {
		e.level++
		e.lockedUntil = l.now().Add(mfaLockDurationFor(e.level))
		locked = true
	}
	return e.fails, e.lockedUntil, e.level, locked
}

func (s *Server) auditMFALockout(r *http.Request, userID string, fails int, until time.Time, level int) {
	if s.audit == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	_ = s.audit(ctx, "auth", "totp_lockout", map[string]any{
		"user_id": userID, "failures": fails, "locked_until": until.UnixMilli(), "client_ip": clientIP(r),
		"lock_level": level, "lock_seconds": int64(mfaLockDurationFor(level) / time.Second),
	})
}

// unlockMFA clears the second-factor lock after a credential reset and audits
// it only when a failure count or lock was actually dropped. adminID is the
// operator of an administrator reset and empty otherwise.
func (s *Server) unlockMFA(r *http.Request, userID, reason, adminID string) {
	if s.mfaLock == nil || !s.mfaLock.clear(userID) || s.audit == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	payload := map[string]any{"user_id": userID, "reason": reason, "client_ip": clientIP(r)}
	if adminID != "" {
		payload["admin_id"] = adminID
	}
	_ = s.audit(ctx, "auth", "totp_unlock", payload)
}

// mfaAttempt runs one second-factor verification under the per-user lock. It
// reports locked without calling verify while the user is locked; a Forbidden
// error counts as a wrong code, any other error releases the reservation.
func (s *Server) mfaAttempt(r *http.Request, userID string, verify func() error) (locked bool, err error) {
	if !s.mfaLock.begin(userID) {
		return true, nil
	}
	err = verify()
	switch {
	case err == nil:
		s.mfaLock.success(userID)
	case isForbiddenError(err):
		if fails, until, level, crossed := s.mfaLock.failure(userID); crossed {
			s.auditMFALockout(r, userID, fails, until, level)
		}
	default:
		s.mfaLock.release(userID)
	}
	return false, err
}

func isForbiddenError(err error) bool {
	var appErr *ipc.Error
	return errors.As(err, &appErr) && appErr.Code == ipc.CodeForbidden
}

func writeMFALocked(w http.ResponseWriter) {
	writeAccountError(w, http.StatusTooManyRequests, ipc.NewError(ipc.CodeForbidden, mfaLockedMessage))
}

type accountMFABeginView struct {
	MFARequired bool   `json:"mfa_required"`
	Ticket      string `json:"ticket"`
	ExpiresAt   int64  `json:"expires_at"`
}

type accountTOTPLoginRequest struct {
	Ticket string `json:"ticket"`
	Code   string `json:"code"`
}

func mfaThrottleKey(r *http.Request, ticket string) string {
	return "mfa|" + ticket + "|" + clientIP(r)
}

// serveAccountTOTPLogin is the second onboarding step for TOTP-bound users: ticket + TOTP code (or recovery code) in exchange for a session.
func (s *Server) serveAccountTOTPLogin(w http.ResponseWriter, r *http.Request) {
	var request accountTOTPLoginRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	entry, ok := s.mfaTickets.get(request.Ticket)
	if !ok {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "登录状态已过期，请重新登录"))
		return
	}
	key := mfaThrottleKey(r, request.Ticket)
	if !s.accountThrottle.mfa.Allow(key) {
		writeAccountError(w, http.StatusTooManyRequests, ipc.NewError(ipc.CodeForbidden, "验证尝试过于频繁，请稍后再试"))
		return
	}
	if !s.mfaLock.begin(entry.userID) {
		writeMFALocked(w)
		return
	}
	valid, err := s.accounts.VerifyTOTPLoginCode(r.Context(), entry.userID, request.Code)
	if err != nil {
		s.mfaLock.release(entry.userID)
		writeAccountFailure(w, err)
		return
	}
	if !valid {
		s.accountThrottle.mfa.RecordFailure(key)
		if fails, until, level, locked := s.mfaLock.failure(entry.userID); locked {
			s.auditMFALockout(r, entry.userID, fails, until, level)
		}
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "验证码错误"))
		return
	}
	s.mfaLock.success(entry.userID)
	s.accountThrottle.mfa.RecordSuccess(key)
	if entry.loginKey != "" {
		s.accountThrottle.login.RecordSuccess(entry.loginKey)
		s.knownLoginClients.Add(entry.loginKey)
	}
	s.mfaTickets.delete(request.Ticket)
	user, err := s.accounts.GetUser(r.Context(), entry.userID)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	s.accountThrottle.loginUser.RecordSuccess(loginUserThrottleKey(user.Username))
	s.accountRevocationMu.Lock()
	defer s.accountRevocationMu.Unlock()
	s.writeAccountSession(w, r, user, entry.deviceID, entry.epoch)
}

type accountTOTPStatusView struct {
	Enabled      bool `json:"enabled"`
	Pending      bool `json:"pending"`
	MFARequired  bool `json:"mfa_required"`
	RecoveryLeft int  `json:"recovery_codes_left"`
}

func (s *Server) serveAccountTOTPStatus(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	enabled, pending, recoveryLeft, err := s.accounts.TOTPStatus(r.Context(), identity.UserID)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	required, err := s.accounts.MFARequired(r.Context())
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, accountTOTPStatusView{Enabled: enabled, Pending: pending, MFARequired: required, RecoveryLeft: recoveryLeft})
}

type accountTOTPSetupView struct {
	Secret     string `json:"secret"`
	OTPAuthURI string `json:"otpauth_uri"`
}

type accountTOTPSetupRequest struct {
	// Reverify is the re-verification credential for rebinding a bound user: current TOTP code, recovery code, or login password.
	Reverify string `json:"reverify"`
}

func (s *Server) serveAccountTOTPSetup(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	var request accountTOTPSetupRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	// The rebind re-verification credential can be brute-forced (6-digit TOTP code), so it must be throttled; the next attempt is allowed only after a successful bind.
	key := "mfa-setup|" + identity.UserID
	if !s.accountThrottle.mfa.Allow(key) {
		writeAccountError(w, http.StatusTooManyRequests, ipc.NewError(ipc.CodeForbidden, "尝试过于频繁，请稍后再试"))
		return
	}
	// Serialize with revocations: the pending binding's epoch note and the
	// setup write must not interleave with a revocation bump.
	s.accountRevocationMu.Lock()
	defer s.accountRevocationMu.Unlock()
	if err := s.revalidateTOTPBindingSession(r); err != nil {
		writeAccountFailure(w, err)
		return
	}
	var secret, uri string
	var err error
	// Only a rebind with a supplied credential verifies a second factor; that
	// check shares the login lockout so it cannot be used to bypass it.
	enabled, enabledErr := s.accounts.TOTPEnabled(r.Context(), identity.UserID)
	if enabledErr != nil {
		writeAccountFailure(w, enabledErr)
		return
	}
	if enabled && request.Reverify != "" {
		locked, attemptErr := s.mfaAttempt(r, identity.UserID, func() (verifyErr error) {
			secret, uri, verifyErr = s.accounts.BeginTOTPSetup(r.Context(), identity.UserID, request.Reverify)
			return verifyErr
		})
		if locked {
			writeMFALocked(w)
			return
		}
		err = attemptErr
	} else {
		secret, uri, err = s.accounts.BeginTOTPSetup(r.Context(), identity.UserID, request.Reverify)
	}
	if err != nil {
		// A missing re-verification credential is a client flow problem and is not counted toward brute-force throttling; only wrong credentials count.
		if request.Reverify != "" {
			s.accountThrottle.mfa.RecordFailure(key)
		}
		writeAccountFailure(w, err)
		return
	}
	s.accountThrottle.mfa.RecordSuccess(key)
	writeAccountJSON(w, http.StatusOK, accountTOTPSetupView{Secret: secret, OTPAuthURI: uri})
}

// revalidateTOTPBindingSession requires accountRevocationMu: a request body
// can arrive after the session admitted by accountGuard has been revoked.
func (s *Server) revalidateTOTPBindingSession(r *http.Request) error {
	identity := accountIdentityFrom(r.Context())
	token, ok := sessionCookie(r)
	if !ok || identity == nil {
		return ipc.NewError(ipc.CodeForbidden, sessionReloginMessage)
	}
	return s.validateAccountSession(r.Context(), token, identity, func() {
		s.cleanupAccountLocked(r.Context(), identity.UserID, identity.SessionID)
	})
}

type accountTOTPConfirmRequest struct {
	Code string `json:"code"`
}

func (s *Server) serveAccountTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	var request accountTOTPConfirmRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	// The confirmation code is a 6-digit number and must be throttled against brute force; the next bind attempt is allowed only after a successful bind.
	key := "mfa-confirm|" + identity.UserID
	if !s.accountThrottle.mfa.Allow(key) {
		writeAccountError(w, http.StatusTooManyRequests, ipc.NewError(ipc.CodeForbidden, "尝试过于频繁，请稍后再试"))
		return
	}
	// Serialize with revocations: the pending-epoch check and the confirm
	// transaction must span the same boundary as an epoch bump, so a
	// revocation cannot land between the check and the commit.
	s.accountRevocationMu.Lock()
	defer s.accountRevocationMu.Unlock()
	if err := s.revalidateTOTPBindingSession(r); err != nil {
		writeAccountFailure(w, err)
		return
	}
	var codes []string
	locked, err := s.mfaAttempt(r, identity.UserID, func() (confirmErr error) {
		codes, confirmErr = s.accounts.ConfirmTOTPSetup(r.Context(), identity.UserID, request.Code)
		return confirmErr
	})
	if locked {
		writeMFALocked(w)
		return
	}
	if err != nil {
		s.accountThrottle.mfa.RecordFailure(key)
		writeAccountFailure(w, err)
		return
	}
	s.accountThrottle.mfa.RecordSuccess(key)
	writeAccountJSON(w, http.StatusOK, map[string][]string{"recovery_codes": codes})
}

type accountTOTPDisableRequest struct {
	Code string `json:"code"`
}

func (s *Server) serveAccountTOTPDisable(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	var request accountTOTPDisableRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	key := "mfa-disable|" + identity.UserID
	if !s.accountThrottle.mfa.Allow(key) {
		writeAccountError(w, http.StatusTooManyRequests, ipc.NewError(ipc.CodeForbidden, "尝试过于频繁，请稍后再试"))
		return
	}
	locked, err := s.mfaAttempt(r, identity.UserID, func() error {
		return s.accounts.DisableTOTP(r.Context(), identity.UserID, request.Code)
	})
	if locked {
		writeMFALocked(w)
		return
	}
	if err != nil {
		s.accountThrottle.mfa.RecordFailure(key)
		writeAccountFailure(w, err)
		return
	}
	s.accountThrottle.mfa.RecordSuccess(key)
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
