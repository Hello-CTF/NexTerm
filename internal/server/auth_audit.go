package server

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// Account audit records use source "auth". A record about the caller's own
// account is owned by that user, an administrator action by the operating
// administrator, so each shows in its owner's audit view. A failed login for a
// name that matches no account has no owner. Payloads carry identifiers and
// field names only, never passwords, tokens, TOTP secrets or recovery codes.
const (
	authAuditSource       = "auth"
	authAuditUserAgentMax = 160

	loginFailureAuditWindow = 15 * time.Minute
	// loginFailureAuditPerKey bounds the failure records of one account (or
	// attempted name) from one client address per window; later failures are
	// counted and reported as suppressed by the first record of the next window.
	loginFailureAuditPerKey = 5
	// loginFailureAuditGlobal bounds the failure records per window across all
	// keys, so a distributed attempt cannot flood audit_log or push older rows
	// out under a count-based retention limit.
	loginFailureAuditGlobal  = 200
	loginFailureAuditMaxKeys = 4096
)

func truncateUserAgent(value string) string {
	if len(value) <= authAuditUserAgentMax {
		return value
	}
	cut := authAuditUserAgentMax
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

// auditAuth writes one account audit record owned by ownerID with the client
// address and a truncated user agent. A write failure is logged and never
// fails the request.
func (s *Server) auditAuth(r *http.Request, ownerID, kind string, payload map[string]any) {
	if s.audit == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["client_ip"] = clientIP(r)
	if userAgent := truncateUserAgent(r.UserAgent()); userAgent != "" {
		payload["user_agent"] = userAgent
	}
	ctx, cancel := context.WithTimeout(ipc.WithUserID(context.WithoutCancel(r.Context()), ownerID), 5*time.Second)
	defer cancel()
	if err := s.audit(ctx, authAuditSource, kind, payload); err != nil {
		s.logger.Warn("write account audit record", "kind", kind, "error", err)
	}
}

// auditSelf records an action a user took on their own account.
func (s *Server) auditSelf(r *http.Request, userID, kind string, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["actor_id"] = userID
	payload["target_user_id"] = userID
	s.auditAuth(r, userID, kind, payload)
}

func (s *Server) auditLoginSuccess(r *http.Request, userID, sessionID, method string) {
	if sessionID == "" {
		return
	}
	s.auditSelf(r, userID, "login_success", map[string]any{"session_id": sessionID, "method": method})
}

// auditLoginFailure records a rejected password or second-factor step,
// aggregated per account (or attempted name) and client address.
func (s *Server) auditLoginFailure(r *http.Request, stage, username, userID, reason string) {
	if s.audit == nil || s.loginFailureAudit == nil {
		return
	}
	payload := map[string]any{"stage": stage, "reason": reason}
	owner := ""
	var key string
	if userID != "" {
		owner = userID
		key = "u|" + userID + "|" + clientIP(r)
		payload["target_user_id"] = userID
	} else {
		pseudonym := ""
		if s.accounts != nil {
			pseudonym = s.accounts.AuditPseudonym(username)
		}
		key = "n|" + pseudonym + "|" + clientIP(r)
		payload["username_hash"] = pseudonym
		payload["username_length"] = utf8.RuneCountInString(username)
	}
	record, failures, suppressed := s.loginFailureAudit.admit(key)
	if !record {
		return
	}
	payload["failures"] = failures
	if suppressed > 0 {
		payload["suppressed_before"] = suppressed
	}
	s.auditAuth(r, owner, "login_failure", payload)
}

// auditPasswordLoginFailure classifies a failed password step without
// revealing anything about the attempt beyond whether the name is an account.
func (s *Server) auditPasswordLoginFailure(r *http.Request, username string) {
	if s.audit == nil {
		return
	}
	user, err := s.accounts.GetUserByUsername(r.Context(), username)
	if err != nil || user == nil {
		s.auditLoginFailure(r, "password", username, "", "unknown_user")
		return
	}
	reason := "rejected"
	if user.State == account.StateDisabled {
		reason = "disabled"
	}
	s.auditLoginFailure(r, "password", username, user.ID, reason)
}

type loginFailureAuditEntry struct {
	start      time.Time
	failures   int
	recorded   int
	suppressed int
	carried    int
}

type loginFailureAuditor struct {
	mu          sync.Mutex
	now         func() time.Time
	windowStart time.Time
	global      int
	entries     map[string]*loginFailureAuditEntry
}

func newLoginFailureAuditor() *loginFailureAuditor {
	return &loginFailureAuditor{now: time.Now, entries: map[string]*loginFailureAuditEntry{}}
}

// admit counts one failure for key and reports whether it gets a record, the
// failures of key in the current window, and the failures of the previous
// window that got no record.
func (a *loginFailureAuditor) admit(key string) (record bool, failures, suppressed int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if now.Sub(a.windowStart) >= loginFailureAuditWindow {
		a.windowStart = now
		a.global = 0
	}
	entry := a.entries[key]
	if entry == nil || now.Sub(entry.start) >= loginFailureAuditWindow {
		carried := 0
		if entry != nil {
			carried = entry.suppressed + entry.carried
		}
		a.makeRoom(now)
		entry = &loginFailureAuditEntry{start: now, carried: carried}
		a.entries[key] = entry
	}
	entry.failures++
	if entry.recorded >= loginFailureAuditPerKey || a.global >= loginFailureAuditGlobal {
		entry.suppressed++
		return false, entry.failures, 0
	}
	entry.recorded++
	a.global++
	suppressed, entry.carried = entry.carried, 0
	return true, entry.failures, suppressed
}

func (a *loginFailureAuditor) makeRoom(now time.Time) {
	if len(a.entries) < loginFailureAuditMaxKeys {
		return
	}
	for key, entry := range a.entries {
		if now.Sub(entry.start) >= loginFailureAuditWindow {
			delete(a.entries, key)
		}
	}
	if len(a.entries) < loginFailureAuditMaxKeys {
		return
	}
	oldestKey := ""
	var oldest time.Time
	for key, entry := range a.entries {
		if oldestKey == "" || entry.start.Before(oldest) {
			oldestKey, oldest = key, entry.start
		}
	}
	delete(a.entries, oldestKey)
}

type adminAuditNoteContextKey struct{}

// adminAuditNote lets an administrator handler name the affected user and the
// changed fields for the record its route writes after a successful response.
type adminAuditNote struct {
	target string
	fields []string
}

func noteAdminAudit(r *http.Request, target string, fields ...string) {
	note, ok := r.Context().Value(adminAuditNoteContextKey{}).(*adminAuditNote)
	if !ok {
		return
	}
	if target != "" {
		note.target = target
	}
	note.fields = append(note.fields, fields...)
}

type auditStatusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *auditStatusRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditStatusRecorder) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

// auditAdminRoute records a successful administrator write as "admin_<name>",
// owned by the operating administrator.
func (s *Server) auditAdminRoute(name string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		note := &adminAuditNote{}
		recorder := &auditStatusRecorder{ResponseWriter: w}
		r = r.WithContext(context.WithValue(r.Context(), adminAuditNoteContextKey{}, note))
		next(recorder, r)
		if recorder.status < 200 || recorder.status >= 300 {
			return
		}
		actor := ""
		if identity := accountIdentityFrom(r.Context()); identity != nil {
			actor = identity.UserID
		}
		payload := map[string]any{"actor_id": actor, "route": name}
		target := note.target
		if target == "" {
			target = r.PathValue("id")
		}
		if target != "" {
			payload["target_user_id"] = target
		}
		if len(note.fields) > 0 {
			payload["fields"] = sortedUnique(note.fields)
		}
		s.auditAuth(r, actor, "admin_"+name, payload)
	}
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// auditRecoveryCodeUse records that a one-time recovery code was consumed;
// the code itself is never recorded.
func (s *Server) auditRecoveryCodeUse(r *http.Request, userID, purpose string) {
	payload := map[string]any{"purpose": purpose}
	if s.accounts != nil {
		if _, _, left, err := s.accounts.TOTPStatus(r.Context(), userID); err == nil {
			payload["recovery_codes_left"] = left
		}
	}
	s.auditSelf(r, userID, "totp_recovery_code_used", payload)
}
