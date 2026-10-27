package fleetserver

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// SessionListFunc lists the durable sessions on userID's devices, waiting up
// to wait for devices that are still answering. The result is written as the
// response body.
type SessionListFunc func(ctx context.Context, userID string, wait time.Duration) (any, error)

const (
	// sessionListAuditInterval coalesces the audit of session listings: a
	// client lists every few seconds while it is open, so one record per
	// account per interval says that it did without flooding the log.
	sessionListAuditInterval = 10 * time.Minute
	sessionListMaxWait       = 5 * time.Second
)

type sessionListState struct {
	mu        sync.Mutex
	list      SessionListFunc
	auditedAt map[string]int64
}

// SetSessionLister installs the listing behind GET /fleet/sessions; without
// one the route answers unsupported.
func (s *Service) SetSessionLister(list SessionListFunc) {
	s.sessionList.mu.Lock()
	s.sessionList.list = list
	s.sessionList.mu.Unlock()
}

// serveSessionList answers one request with the attention state of the
// sessions on every device of the caller, so a desktop or phone does not dial
// one terminal bridge per device. It is read-only: it opens no terminal and
// is audited as terminal_list at most once per account per interval.
func (s *Service) serveSessionList(w http.ResponseWriter, r *http.Request) {
	identity := identityFrom(r)
	s.sessionList.mu.Lock()
	list := s.sessionList.list
	s.sessionList.mu.Unlock()
	if list == nil {
		writeFleetJSON(w, http.StatusNotImplemented, ipc.Failure(ipc.NewError(ipc.CodeUnsupported, "服务器不提供会话列表")))
		return
	}
	wait := time.Duration(0)
	if raw := r.URL.Query().Get("wait_ms"); raw != "" {
		ms, err := strconv.Atoi(raw)
		if err != nil || ms < 0 {
			writeFleetJSON(w, http.StatusBadRequest, ipc.Failure(ipc.NewError(ipc.CodeBadParam, "wait_ms 无效")))
			return
		}
		wait = min(time.Duration(ms)*time.Millisecond, sessionListMaxWait)
	}
	s.auditSessionList(r.Context(), identity)
	result, err := list(r.Context(), identity.UserID, wait)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, result)
}

func (s *Service) auditSessionList(ctx context.Context, identity *account.Identity) {
	now := s.now()
	s.sessionList.mu.Lock()
	if s.sessionList.auditedAt == nil {
		s.sessionList.auditedAt = make(map[string]int64)
	}
	last, seen := s.sessionList.auditedAt[identity.UserID]
	due := !seen || now-last >= sessionListAuditInterval.Milliseconds()
	if due {
		s.sessionList.auditedAt[identity.UserID] = now
	}
	s.sessionList.mu.Unlock()
	if due {
		_ = s.audit(ctx, auditKindDeviceTerminal, auditPayload{Action: "terminal_list", Requester: identity.UserID, Outcome: "allow"})
	}
}

// CheckDeviceTerminal applies the owner, revocation and terminal-switch rules
// of AuthorizeDeviceTerminal without writing an audit, for read-only listings
// that dial a device from an earlier device list.
func (s *Service) CheckDeviceTerminal(ctx context.Context, userID, deviceID string) error {
	if userID == "" {
		return ipc.NewError(ipc.CodeForbidden, "会话无效或缺失，请重新登录")
	}
	if _, err := s.canAccess(ctx, &account.Identity{UserID: userID}, deviceID); err != nil {
		return err
	}
	desired, err := s.AgentDesiredConfig(ctx, deviceID)
	if err != nil {
		return err
	}
	if !desired.TerminalEnabled {
		return ipc.NewError(ipc.CodeForbidden, "设备已关闭终端访问")
	}
	return nil
}
