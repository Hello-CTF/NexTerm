package fleetserver

import (
	"context"
	"crypto/ed25519"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm/clause"
)

const (
	// p2pKeyRotation is how long the newest signing key signs before a new one is generated.
	p2pKeyRotation = 30 * 24 * time.Hour
	// p2pKeyOverlap keeps a superseded key published for longer than a grant lives plus the 10-minute config
	// cache of clients and agents, so grants signed just before a rotation still verify.
	p2pKeyOverlap = time.Hour
	// p2pKeyRetention keeps old keys for recognizing late audit uploads; older ones are deleted.
	p2pKeyRetention = 2 * p2pKeyRotation
	// p2pKeyCacheTTL bounds how long an instance serves cached keys before rereading the shared table.
	p2pKeyCacheTTL = time.Minute
)

// p2pKeyRing loads the grant signing keys from the database, creating and rotating them on demand. Instances
// sharing the database converge on the same set: each signs with the newest stored key.
type p2pKeyRing struct {
	mu       sync.Mutex
	loadedAt int64
	rows     []*store.P2PSigningKey
}

func (k *p2pKeyRing) load(ctx context.Context, s *Service) ([]*store.P2PSigningKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := s.now()
	if k.rows != nil && now-k.loadedAt < p2pKeyCacheTTL.Milliseconds() && now-k.rows[0].CreatedAt < p2pKeyRotation.Milliseconds() {
		return k.rows, nil
	}
	table := query.Use(s.db).P2PSigningKey
	rows, err := table.WithContext(ctx).Order(table.CreatedAt.Desc(), table.KID).Find()
	if err != nil {
		return nil, dbError(err)
	}
	if len(rows) == 0 || now-rows[0].CreatedAt >= p2pKeyRotation.Milliseconds() {
		key, err := grant.GenerateSigningKey(nil)
		if err != nil {
			return nil, dbError(err)
		}
		row := &store.P2PSigningKey{
			KID: key.KID, Alg: grant.Alg, Seed: key.Private.Seed(),
			PublicKey: key.Public().Pub.String(), CreatedAt: now,
		}
		if err := table.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row); err != nil {
			return nil, dbError(err)
		}
		if _, err := table.WithContext(ctx).Where(table.CreatedAt.Lt(now - p2pKeyRetention.Milliseconds())).Delete(); err != nil {
			return nil, dbError(err)
		}
		if rows, err = table.WithContext(ctx).Order(table.CreatedAt.Desc(), table.KID).Find(); err != nil {
			return nil, dbError(err)
		}
	}
	k.rows, k.loadedAt = rows, now
	return rows, nil
}

// signing returns the newest key.
func (k *p2pKeyRing) signing(ctx context.Context, s *Service) (grant.SigningKey, error) {
	rows, err := k.load(ctx, s)
	if err != nil {
		return grant.SigningKey{}, err
	}
	return grant.SigningKey{KID: rows[0].KID, Private: ed25519.NewKeyFromSeed(rows[0].Seed)}, nil
}

// published returns the keys clients and agents trust: the newest one and those superseded within the overlap.
func (k *p2pKeyRing) published(ctx context.Context, s *Service) ([]grant.PublicKey, error) {
	rows, err := k.load(ctx, s)
	if err != nil {
		return nil, err
	}
	keys := []grant.PublicKey{}
	supersededAt := s.now()
	for _, row := range rows {
		if len(keys) > 0 && s.now()-supersededAt > p2pKeyOverlap.Milliseconds() {
			break
		}
		if public, err := grant.ParseKey(row.PublicKey); err == nil {
			keys = append(keys, grant.PublicKey{KID: row.KID, Alg: row.Alg, Pub: public})
		}
		supersededAt = row.CreatedAt
	}
	return keys, nil
}

// all returns every retained key, for recognizing grants named in late audit uploads.
func (k *p2pKeyRing) all(ctx context.Context, s *Service) ([]grant.PublicKey, error) {
	rows, err := k.load(ctx, s)
	if err != nil {
		return nil, err
	}
	keys := make([]grant.PublicKey, 0, len(rows))
	for _, row := range rows {
		if public, err := grant.ParseKey(row.PublicKey); err == nil {
			keys = append(keys, grant.PublicKey{KID: row.KID, Alg: row.Alg, Pub: public})
		}
	}
	return keys, nil
}
