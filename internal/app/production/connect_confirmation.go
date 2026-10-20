package production

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// Per-user explicit opt-in settings for connecting disconnected assets on the
// broadcast and target-set surfaces. Each surface has its own pair so one
// surface's opt-in never authorizes another (the cron opt-in is not a global
// grant); both default to off.
const (
	BroadcastConnectOptInSettingKey           = "ai.broadcast_connect_opt_in"
	BroadcastConnectProductionOptInSettingKey = "ai.broadcast_connect_production"
	TargetSetConnectOptInSettingKey           = "ai.target_set_connect_opt_in"
	TargetSetConnectProductionOptInSettingKey = "ai.target_set_connect_production"
)

func BroadcastConnectOptInSettingKeyForUser(ctx context.Context) string {
	return connectOptInSettingKeyForUser(ctx, BroadcastConnectOptInSettingKey)
}

func BroadcastConnectProductionOptInSettingKeyForUser(ctx context.Context) string {
	return connectOptInSettingKeyForUser(ctx, BroadcastConnectProductionOptInSettingKey)
}

func TargetSetConnectOptInSettingKeyForUser(ctx context.Context) string {
	return connectOptInSettingKeyForUser(ctx, TargetSetConnectOptInSettingKey)
}

func TargetSetConnectProductionOptInSettingKeyForUser(ctx context.Context) string {
	return connectOptInSettingKeyForUser(ctx, TargetSetConnectProductionOptInSettingKey)
}

func connectOptInSettingKeyForUser(ctx context.Context, key string) string {
	if userID, ok := ipc.UserIDFromContext(ctx); ok {
		return key + ".user." + userID
	}
	return key
}

// surfaceConnectEvidence reuses the cron connect evidence but reads the
// surface's own production opt-in setting.
type surfaceConnectEvidence struct {
	cronConnectEvidence
	productionOptInKey func(context.Context) string
}

func (e surfaceConnectEvidence) ProductionConfirmed(ctx context.Context, _ cron.Trigger, _ string) (bool, error) {
	return cronConnectSetting(ctx, e.services.Store, e.productionOptInKey(ctx))
}

// newSurfaceConnector builds the R-07 connect gate for the broadcast and
// target-set surfaces: the same evidence, authorization, and strict host-key
// dial as cron, with the surface's own per-user opt-in and production
// confirmation settings.
func newSurfaceConnector(services *ProductionServices, optInKey, productionOptInKey func(context.Context) string) *cron.Connector {
	return &cron.Connector{
		OptIn: func(ctx context.Context, _ cron.Trigger) (bool, error) {
			return cronConnectSetting(ctx, services.Store, optInKey(ctx))
		},
		Evidence: surfaceConnectEvidence{
			cronConnectEvidence: cronConnectEvidence{services: services},
			productionOptInKey:  productionOptInKey,
		},
		Dial: func(ctx context.Context, trigger cron.Trigger, assetID string) (string, error) {
			return dialAssetStrict(ctx, services, trigger.OwnerID, assetID)
		},
		Settings: services.Store,
	}
}

// authorizeSurfaceConnect evaluates the R-07 gates for the surface without
// dialing: opt-in, owner match, known host key, vault readiness, and the
// surface's production confirmation.
func authorizeSurfaceConnect(ctx context.Context, services *ProductionServices, optInKey, productionOptInKey func(context.Context) string, ownerID, assetID string) error {
	optIn, err := cronConnectSetting(ctx, services.Store, optInKey(ctx))
	if err != nil {
		return err
	}
	if !optIn {
		return guard.ErrConnectOptInRequired
	}
	evidence := surfaceConnectEvidence{
		cronConnectEvidence: cronConnectEvidence{services: services},
		productionOptInKey:  productionOptInKey,
	}
	trigger := cron.Trigger{OwnerID: ownerID}
	ownerMatch, err := evidence.OwnerMatch(ctx, trigger, assetID)
	if err != nil {
		return err
	}
	hostKey, err := evidence.HostKey(ctx, trigger, assetID)
	if err != nil {
		return err
	}
	vaultReady, err := evidence.VaultReady(ctx, trigger, assetID)
	if err != nil {
		return err
	}
	environment, err := evidence.Environment(ctx, trigger, assetID)
	if err != nil {
		return err
	}
	productionConfirmed, err := evidence.ProductionConfirmed(ctx, trigger, assetID)
	if err != nil {
		return err
	}
	return guard.AuthorizeConnect(ctx, services.Store, guard.ConnectAuthorization{
		OptIn: optIn, OwnerMatch: ownerMatch, HostKey: hostKey, VaultReady: vaultReady,
		Environment: environment, ProductionConfirmed: productionConfirmed,
	})
}

// connectSurfaceAsset authorizes and dials a disconnected asset for the
// surface, mapping the no-opt-in decline to the caller's original error so the
// default stays the pre-R-07 behavior.
func connectSurfaceAsset(ctx context.Context, connector *cron.Connector, ownerID, assetID string, originalErr error) (string, error) {
	sessionID, err := connector.Connect(ctx, cron.Trigger{OwnerID: ownerID}, assetID)
	if err != nil {
		if errors.Is(err, guard.ErrConnectOptInRequired) {
			return "", originalErr
		}
		return "", err
	}
	return sessionID, nil
}
