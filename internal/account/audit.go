package account

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync/atomic"
)

const auditPseudonymPurpose = "nexterm-audit-username-v1"

// AuditPseudonym returns a keyed, truncated digest of an attempted login name
// that matches no account. Audit records carry it instead of the typed text,
// because a user who types a password into the username field must not have
// it stored; repeated attempts with the same name still share a pseudonym.
// The key is the CSRF server secret, so pseudonyms are stable across restarts
// only when the out-of-database key file is configured.
func (a *Accounts) AuditPseudonym(value string) string {
	key, err := a.csrfKey()
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(auditPseudonymPurpose))
	mac.Write([]byte{0})
	mac.Write([]byte(strings.ToLower(strings.TrimSpace(value))))
	return hex.EncodeToString(mac.Sum(nil)[:8])
}

type recoveryUseContextKey struct{}

// TrackRecoveryCodeUse returns a context whose second-factor checks report
// whether they consumed a recovery code, so the caller can audit that use.
func TrackRecoveryCodeUse(ctx context.Context) (context.Context, func() bool) {
	used := &atomic.Bool{}
	return context.WithValue(ctx, recoveryUseContextKey{}, used), used.Load
}

func markRecoveryCodeUsed(ctx context.Context) {
	if used, ok := ctx.Value(recoveryUseContextKey{}).(*atomic.Bool); ok {
		used.Store(true)
	}
}
