package db

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// AssetIDOf resolves the verified database asset identity of a live connection
// for owner-scoped grant evaluation. The identity is the DB asset the
// connection was resolved from at Connect time; an SSH tunnel viaAssetId is
// transport-only and never becomes the connection's asset identity.
//
// The second return is false when the connection is unknown, belongs to
// another owner, its login session was revoked, or it was opened from inline
// configuration without a database asset. A false result carries no grant
// authority and must never be treated as an asset ID.
func (s *Service) AssetIDOf(ctx context.Context, connID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.conns[connID] == nil {
		return "", false
	}
	if !connectionRequestAllowed(ctx, s.owners[connID]) {
		return "", false
	}
	if sessionID := ipc.AccountSessionID(ctx); sessionID != "" {
		if _, revoked := s.revokedLoginSessions[sessionID]; revoked {
			return "", false
		}
	}
	assetID, ok := s.assetIDs[connID]
	return assetID, ok
}
