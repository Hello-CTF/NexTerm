package server

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const (
	mfaTicketBytes = 32
	mfaTicketTTL   = 5 * time.Minute
)

type mfaTicket struct {
	userID    string
	deviceID  string
	expiresAt time.Time
	// epoch is the account generation at ticket issue; the confirm re-checks
	// it so a revocation or reset between the two steps fails the login closed.
	epoch uint64
}

// mfaTicketStore holds one-time tickets for "password passed, second factor pending" (memory only).
// A restart invalidates them and the user simply re-enters the password; tickets are random 256-bit values, not enumerable.
type mfaTicketStore struct {
	mu      sync.Mutex
	entries map[string]*mfaTicket
	now     func() time.Time
}

func newMFATicketStore() *mfaTicketStore {
	return &mfaTicketStore{entries: make(map[string]*mfaTicket), now: time.Now}
}

func (s *mfaTicketStore) issue(userID, deviceID string, epoch uint64) (string, time.Time) {
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
	s.entries[ticket] = &mfaTicket{userID: userID, deviceID: deviceID, expiresAt: now.Add(mfaTicketTTL), epoch: epoch}
	return ticket, now.Add(mfaTicketTTL)
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
	valid, err := s.accounts.VerifyTOTPLoginCode(r.Context(), entry.userID, request.Code)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	if !valid {
		s.accountThrottle.mfa.RecordFailure(key)
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "验证码错误"))
		return
	}
	s.accountThrottle.mfa.RecordSuccess(key)
	s.mfaTickets.delete(request.Ticket)
	user, err := s.accounts.GetUser(r.Context(), entry.userID)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
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
	secret, uri, err := s.accounts.BeginTOTPSetup(r.Context(), identity.UserID, request.Reverify)
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
	codes, err := s.accounts.ConfirmTOTPSetup(r.Context(), identity.UserID, request.Code)
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
	if err := s.accounts.DisableTOTP(r.Context(), identity.UserID, request.Code); err != nil {
		s.accountThrottle.mfa.RecordFailure(key)
		writeAccountFailure(w, err)
		return
	}
	s.accountThrottle.mfa.RecordSuccess(key)
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
