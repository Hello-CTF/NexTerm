package fleetserver

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const auditKindDeviceP2PKey = "device_p2p_key"

type p2pKeyAudit struct {
	Action    string `json:"action"`
	DeviceID  string `json:"device_id"`
	Requester string `json:"requester"`
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason,omitempty"`
	PublicKey string `json:"public_key,omitempty"`
	Previous  string `json:"previous_key,omitempty"`
}

func (s *Service) deviceP2PKey(ctx context.Context, deviceID string) (*store.DeviceP2PKey, error) {
	table := query.Use(s.db).DeviceP2PKey
	row, err := table.WithContext(ctx).Where(table.DeviceID.Eq(deviceID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, dbError(err)
	}
	return row, nil
}

// RegisterP2PDeviceKey records the key an authenticated agent presents. The first key is trusted on first use
// (the device credential authenticates it). The trusted key is never overwritten: a different key is kept as
// pending, the device's live grants are revoked, and grants stay refused until the owner confirms the pending key.
// Presenting the trusted key again clears a pending key the owner never confirmed.
func (s *Service) RegisterP2PDeviceKey(ctx context.Context, deviceID string, public grant.Key) (agent.P2PKeyResponse, error) {
	encoded := public.String()
	ctx = ipc.WithUserID(ctx, deviceOwnerID(ctx, query.Use(s.db), deviceID))
	table := query.Use(s.db).DeviceP2PKey
	now := s.now()
	row, err := s.deviceP2PKey(ctx, deviceID)
	if err != nil {
		return agent.P2PKeyResponse{}, err
	}
	created := false
	if row == nil {
		// A concurrent first registration may win the insert; the reread below then compares against its key.
		if err := table.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
			Create(&store.DeviceP2PKey{DeviceID: deviceID, PublicKey: encoded, BoundAt: now}); err != nil {
			return agent.P2PKeyResponse{}, dbError(err)
		}
		if row, err = s.deviceP2PKey(ctx, deviceID); err != nil {
			return agent.P2PKeyResponse{}, err
		}
		if row == nil {
			return agent.P2PKeyResponse{}, ipc.NewError(ipc.CodeNotFound, "设备不存在")
		}
		created = row.PublicKey == encoded
	}
	switch {
	case created:
		_ = s.auditP2P(ctx, auditKindDeviceP2PKey, deviceID, p2pKeyAudit{Action: "p2p_key_bind", DeviceID: deviceID, Requester: deviceID, Outcome: "allow", PublicKey: encoded})
	case row.PublicKey == encoded:
		if row.PendingKey != "" {
			if _, err := table.WithContext(ctx).Where(table.DeviceID.Eq(deviceID), table.PublicKey.Eq(encoded)).
				UpdateColumns(map[string]any{"pending_key": "", "pending_at": nil}); err != nil {
				return agent.P2PKeyResponse{}, dbError(err)
			}
			_ = s.auditP2P(ctx, auditKindDeviceP2PKey, deviceID, p2pKeyAudit{Action: "p2p_key_restore", DeviceID: deviceID, Requester: deviceID, Outcome: "allow", PublicKey: encoded, Previous: row.PendingKey})
		}
	default:
		if row.PendingKey != encoded {
			if _, err := table.WithContext(ctx).Where(table.DeviceID.Eq(deviceID)).
				UpdateColumns(map[string]any{"pending_key": encoded, "pending_at": now}); err != nil {
				return agent.P2PKeyResponse{}, dbError(err)
			}
			_ = s.auditP2P(ctx, auditKindDeviceP2PKey, deviceID, p2pKeyAudit{Action: "p2p_key_change", DeviceID: deviceID, Requester: deviceID, Outcome: "deny", Reason: "confirmation_required", PublicKey: encoded, Previous: row.PublicKey})
		}
		s.revokeP2PGrants(func(entry *activeGrant) bool { return entry.deviceID == deviceID }, p2pRevokeKey, true)
		return agent.P2PKeyResponse{State: agent.P2PKeyStatePending, PublicKey: row.PublicKey}, nil
	}
	return agent.P2PKeyResponse{State: agent.P2PKeyStateBound, PublicKey: encoded}, nil
}

// ConfirmP2PDeviceKey lets the device owner accept the pending key; publicKey must name the pending key, so a
// confirmation never accepts a key that changed after the owner looked at it.
func (s *Service) ConfirmP2PDeviceKey(ctx context.Context, identity *account.Identity, deviceID string, public grant.Key) error {
	if _, err := s.authorize(ctx, identity, deviceID, "p2p_key_confirm", auditKindDeviceP2PKey); err != nil {
		return err
	}
	table := query.Use(s.db).DeviceP2PKey
	encoded := public.String()
	updated, err := table.WithContext(ctx).Where(table.DeviceID.Eq(deviceID), table.PendingKey.Eq(encoded)).
		UpdateColumns(map[string]any{"public_key": encoded, "pending_key": "", "pending_at": nil, "confirmed_at": s.now()})
	if err != nil {
		return dbError(err)
	}
	if updated.RowsAffected == 0 {
		return ipc.NewError(ipc.CodeBadParam, "设备没有与之匹配的待确认直连密钥，请刷新后重试")
	}
	return nil
}

// p2pKeyView is the device list's view of the device key.
type p2pKeyView struct {
	State      string `json:"state"`
	PublicKey  string `json:"public_key,omitempty"`
	PendingKey string `json:"pending_key,omitempty"`
	PendingAt  int64  `json:"pending_at,omitempty"`
}

func newP2PKeyView(row *store.DeviceP2PKey) *p2pKeyView {
	if row == nil || row.PublicKey == "" {
		return &p2pKeyView{State: agent.P2PKeyStateNone}
	}
	view := &p2pKeyView{State: agent.P2PKeyStateBound, PublicKey: row.PublicKey}
	if row.PendingKey != "" {
		view.State, view.PendingKey = agent.P2PKeyStatePending, row.PendingKey
		if row.PendingAt != nil {
			view.PendingAt = *row.PendingAt
		}
	}
	return view
}

// p2pKeyViews loads the key views of deviceIDs.
func (s *Service) p2pKeyViews(ctx context.Context, deviceIDs []string) (map[string]*p2pKeyView, error) {
	views := make(map[string]*p2pKeyView, len(deviceIDs))
	if len(deviceIDs) == 0 {
		return views, nil
	}
	table := query.Use(s.db).DeviceP2PKey
	rows, err := table.WithContext(ctx).Where(table.DeviceID.In(deviceIDs...)).Find()
	if err != nil {
		return nil, dbError(err)
	}
	for _, row := range rows {
		views[row.DeviceID] = newP2PKeyView(row)
	}
	for _, id := range deviceIDs {
		if views[id] == nil {
			views[id] = newP2PKeyView(nil)
		}
	}
	return views, nil
}
