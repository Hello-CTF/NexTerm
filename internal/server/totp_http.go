package server

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

const (
	mfaTicketBytes   = 32
	mfaTicketTTL     = 5 * time.Minute
	mfaTicketMaxIdle = time.Hour
)

type mfaTicket struct {
	userID    string
	username  string
	deviceID  string
	expiresAt time.Time
	lastSeen  time.Time
}

// mfaTicketStore 保存「密码已通过、待第二因子」的一次性票据(仅内存)。
// 重启即失效,用户重新输入密码即可;票据随机 256 位,不可枚举。
type mfaTicketStore struct {
	mu      sync.Mutex
	entries map[string]*mfaTicket
	now     func() time.Time
}

func newMFATicketStore() *mfaTicketStore {
	return &mfaTicketStore{entries: make(map[string]*mfaTicket), now: time.Now}
}

func (s *mfaTicketStore) issue(userID, username, deviceID string) (string, time.Time) {
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
	s.entries[ticket] = &mfaTicket{userID: userID, username: username, deviceID: deviceID, expiresAt: now.Add(mfaTicketTTL), lastSeen: now}
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
	entry.lastSeen = now
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
		if now.After(entry.expiresAt) || now.Sub(entry.lastSeen) > mfaTicketMaxIdle {
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

// serveAccountTOTPLogin 是已绑定 TOTP 用户的第二步入职: 票据 + 动态码(或恢复码)换会话。
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
	s.writeAccountSession(w, r, user, entry.deviceID)
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
	// Reverify 是已绑定用户的换绑重验凭据: 当前动态码、恢复码或登录密码。
	Reverify string `json:"reverify"`
}

func (s *Server) serveAccountTOTPSetup(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	var request accountTOTPSetupRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	// 换绑重验凭可能被爆破(动态码 6 位), 必须限流; 绑定成功后才允许下一次尝试。
	key := "mfa-setup|" + identity.UserID
	if !s.accountThrottle.mfa.Allow(key) {
		writeAccountError(w, http.StatusTooManyRequests, ipc.NewError(ipc.CodeForbidden, "尝试过于频繁，请稍后再试"))
		return
	}
	secret, uri, err := s.accounts.BeginTOTPSetup(r.Context(), identity.UserID, request.Reverify)
	if err != nil {
		// 未携带重验凭据是客户端流程问题, 不计入爆破限流; 错误凭据才计。
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
	// 确认码是 6 位数字,必须限流防爆破; 绑定成功后才允许下一次绑定尝试。
	key := "mfa-confirm|" + identity.UserID
	if !s.accountThrottle.mfa.Allow(key) {
		writeAccountError(w, http.StatusTooManyRequests, ipc.NewError(ipc.CodeForbidden, "尝试过于频繁，请稍后再试"))
		return
	}
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
