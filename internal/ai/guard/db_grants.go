package guard

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
)

// ErrConnAssetNotFound rejects database connection IDs that do not resolve to an
// asset bound by the requesting owner; the error deliberately does not reveal
// whether the connection exists under another owner.
var ErrConnAssetNotFound = errors.New("数据库连接未绑定到当前身份的资产")

// connAssetRegistry maps AI database connection IDs to the asset whose tunnel
// carries them, scoped per owner: a connection bound by one owner never
// resolves for another, so grant decisions for database tools always apply to
// the real asset identity of the requesting owner.
type connAssetRegistry struct {
	shared bool

	mu      sync.RWMutex
	byOwner map[string]map[string]string
}

func newConnAssetRegistry(shared bool) *connAssetRegistry {
	return &connAssetRegistry{shared: shared, byOwner: make(map[string]map[string]string)}
}

func (r *connAssetRegistry) bind(ctx context.Context, connID, assetID string) error {
	connID = strings.TrimSpace(connID)
	assetID = strings.TrimSpace(assetID)
	if connID == "" {
		return errors.New("数据库连接绑定缺少连接 ID")
	}
	if assetID == "" {
		return errors.New("数据库连接绑定缺少资产 ID")
	}
	ownerID, err := owner.ID(ctx, r.shared)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	bindings := r.byOwner[ownerID]
	if bindings == nil {
		bindings = make(map[string]string)
		r.byOwner[ownerID] = bindings
	}
	bindings[connID] = assetID
	return nil
}

func (r *connAssetRegistry) resolve(ctx context.Context, connID string) (string, error) {
	connID = strings.TrimSpace(connID)
	if connID == "" {
		return "", ErrConnAssetNotFound
	}
	ownerID, err := owner.ID(ctx, r.shared)
	if err != nil {
		return "", err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	assetID, ok := r.byOwner[ownerID][connID]
	if !ok {
		return "", ErrConnAssetNotFound
	}
	return assetID, nil
}

func (r *connAssetRegistry) unbind(ctx context.Context, connID string) {
	connID = strings.TrimSpace(connID)
	if connID == "" {
		return
	}
	ownerID, err := owner.ID(ctx, r.shared)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	bindings := r.byOwner[ownerID]
	delete(bindings, connID)
	if len(bindings) == 0 {
		delete(r.byOwner, ownerID)
	}
}

// BindConnAsset records that the owner's database connection connID tunnels
// through assetID; the binding backs owner-scoped grant evaluation for the AI
// database tools.
func (g *Grants) BindConnAsset(ctx context.Context, connID, assetID string) error {
	return g.conns.bind(ctx, connID, assetID)
}

// ResolveConnAsset resolves the owner's database connection to the asset whose
// tunnel carries it; connections bound by another owner do not resolve.
func (g *Grants) ResolveConnAsset(ctx context.Context, connID string) (string, error) {
	return g.conns.resolve(ctx, connID)
}

// UnbindConnAsset drops the owner's database connection binding, typically when
// the connection closes.
func (g *Grants) UnbindConnAsset(ctx context.Context, connID string) {
	g.conns.unbind(ctx, connID)
}

// EvaluateDBResource evaluates a database tool call against the grants of the
// asset whose tunnel carries the connection. A connection that does not resolve
// owner-scoped to an asset is denied, so an unbound or cross-owner connId can
// never inherit another asset's grant.
func (g *Grants) EvaluateDBResource(ctx context.Context, config Config, ruling Ruling, memory *Memory, connID, operation, runID string, resource Resource) Decision {
	assetID, err := g.conns.resolve(ctx, connID)
	if err != nil {
		return Decision{Action: ActionDeny, Ruling: ruling, Reason: err.Error()}
	}
	return g.EvaluateResource(ctx, config, ruling, memory, assetID, operation, runID, resource)
}
