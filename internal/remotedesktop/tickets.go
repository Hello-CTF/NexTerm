package remotedesktop

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const (
	defaultTicketTTL = 60 * time.Second
	maxLiveTickets   = 256
	maxUserTickets   = 16
)

type ticket struct {
	token        string
	userID       string
	loginSession string
	assetID      string
	protocol     string
	expires      time.Time
	released     bool
	// password is a typed password bound by SetPassword (promptPassword assets); it never leaves Go.
	password string
	// checkCtx carries the server-owned login session check for the live connection recheck.
	checkCtx context.Context
}

// ticketStore issues single-use, short-lived tickets bound to user, asset and protocol.
type ticketStore struct {
	mu      sync.Mutex
	now     func() time.Time
	ttl     time.Duration
	tickets map[string]*ticket
}

func newTicketStore(now func() time.Time, ttl time.Duration) *ticketStore {
	if now == nil {
		now = time.Now
	}
	if ttl <= 0 {
		ttl = defaultTicketTTL
	}
	return &ticketStore{now: now, ttl: ttl, tickets: make(map[string]*ticket)}
}

func errTicketInvalid() error {
	return ipc.NewError(ipc.CodeForbidden, "远程桌面票据无效或已过期，请重新连接")
}

func (s *ticketStore) issue(checkCtx context.Context, userID, loginSession, assetID, protocol string) (*ticket, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, ipc.WrapError(ipc.CodeInternal, "生成远程桌面票据失败", err)
	}
	t := &ticket{
		token: base64.RawURLEncoding.EncodeToString(raw), userID: userID, loginSession: loginSession,
		assetID: assetID, protocol: protocol, expires: s.now().Add(s.ttl), checkCtx: checkCtx,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	perUser := 0
	for _, live := range s.tickets {
		if live.userID == userID {
			perUser++
		}
	}
	if perUser >= maxUserTickets || len(s.tickets) >= maxLiveTickets {
		return nil, ipc.NewError(ipc.CodeBadParam, "远程桌面连接请求过于频繁，请稍后再试")
	}
	s.tickets[t.token] = t
	return t, nil
}

func (s *ticketStore) sweepLocked() {
	now := s.now()
	for key, t := range s.tickets {
		if !now.Before(t.expires) {
			delete(s.tickets, key)
		}
	}
}

func (s *ticketStore) ownerMatches(t *ticket, userID, loginSession string) bool {
	if subtle.ConstantTimeCompare([]byte(t.userID), []byte(userID)) != 1 {
		return false
	}
	return t.loginSession == loginSession
}

// consume validates and removes the ticket (single use).
func (s *ticketStore) consume(token, userID, loginSession string) (*ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tickets[token]
	if t == nil || !s.now().Before(t.expires) {
		delete(s.tickets, token)
		return nil, errTicketInvalid()
	}
	if !s.ownerMatches(t, userID, loginSession) {
		return nil, errTicketInvalid()
	}
	delete(s.tickets, token)
	return t, nil
}

// release marks the ticket's secret as released exactly once and leaves the ticket usable for the attach.
func (s *ticketStore) release(token, userID, loginSession, protocol string) (*ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tickets[token]
	if t == nil || !s.now().Before(t.expires) {
		delete(s.tickets, token)
		return nil, errTicketInvalid()
	}
	if !s.ownerMatches(t, userID, loginSession) || t.protocol != protocol {
		return nil, errTicketInvalid()
	}
	if t.released {
		return nil, ipc.NewError(ipc.CodeForbidden, "该票据的凭据已经释放过，请重新连接")
	}
	t.released = true
	copied := *t
	return &copied, nil
}

func (s *ticketStore) dropLoginSession(loginSession string) {
	if loginSession == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, t := range s.tickets {
		if t.loginSession == loginSession {
			delete(s.tickets, key)
		}
	}
}

// setPassword binds a typed password to an unconsumed ticket of the given protocol. It can be set once.
func (s *ticketStore) setPassword(token, userID, loginSession, protocol, password string) (*ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tickets[token]
	if t == nil || !s.now().Before(t.expires) {
		delete(s.tickets, token)
		return nil, errTicketInvalid()
	}
	if !s.ownerMatches(t, userID, loginSession) || t.protocol != protocol {
		return nil, errTicketInvalid()
	}
	if t.password != "" {
		return nil, ipc.NewError(ipc.CodeForbidden, "该票据已设置过密码，请重新连接")
	}
	t.password = password
	copied := *t
	return &copied, nil
}

func (s *ticketStore) dropUser(userID, exceptLoginSession string) {
	if userID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, t := range s.tickets {
		if t.userID == userID && ipc.MatchesRevoked(t.loginSession, exceptLoginSession) {
			delete(s.tickets, key)
		}
	}
}

func (s *ticketStore) clearPassword(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.tickets[token]; t != nil {
		t.password = ""
	}
}
