package cron

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// ConnectOptInSettingKey persists the per-owner explicit opt-in for connecting
// disconnected assets during cron runs (default off).
const ConnectOptInSettingKey = "ai.cron_connect_opt_in"

// ConnectProductionOptInSettingKey persists the per-owner explicit opt-in for
// connecting production-classified assets during cron runs (default off).
const ConnectProductionOptInSettingKey = "ai.cron_connect_production"

func connectOptInSettingKeyForUser(ctx context.Context, key string) string {
	if userID, ok := ipc.UserIDFromContext(ctx); ok {
		return key + ".user." + userID
	}
	return key
}

func ConnectOptInSettingKeyForUser(ctx context.Context) string {
	return connectOptInSettingKeyForUser(ctx, ConnectOptInSettingKey)
}

func ConnectProductionOptInSettingKeyForUser(ctx context.Context) string {
	return connectOptInSettingKeyForUser(ctx, ConnectProductionOptInSettingKey)
}

// ErrAssetDisconnected marks that a cron run's scope asset has no healthy
// connected session. The scope-resolution layer returns it (wrapped in
// AssetDisconnectedError) so the executor can apply the opt-in connect flow
// instead of failing the run.
var ErrAssetDisconnected = errors.New("cron: scope asset is not connected")

// ErrConnectAssetRequired rejects connect attempts without a target asset.
var ErrConnectAssetRequired = errors.New("cron: connect requires an asset ID")

// AssetDisconnectedError carries the disconnected scope asset for the opt-in
// connect flow.
type AssetDisconnectedError struct {
	AssetID string
	Err     error
}

func (e *AssetDisconnectedError) Error() string {
	if e.Err == nil {
		return ErrAssetDisconnected.Error() + ": " + e.AssetID
	}
	return fmt.Sprintf("%s: %s: %v", ErrAssetDisconnected, e.AssetID, e.Err)
}

func (e *AssetDisconnectedError) Unwrap() error { return ErrAssetDisconnected }

// ConnectEvidence resolves the authorization evidence for connecting a
// disconnected asset during a cron run.
type ConnectEvidence interface {
	// OwnerMatch reports whether the asset belongs to the trigger's owner.
	OwnerMatch(context.Context, Trigger, string) (bool, error)
	// HostKey returns the known-host verdict for the asset.
	HostKey(context.Context, Trigger, string) (guard.HostKeyState, error)
	// VaultReady reports whether the asset's credential is available.
	VaultReady(context.Context, Trigger, string) (bool, error)
	// Environment returns the resolved environment name of the asset.
	Environment(context.Context, Trigger, string) (string, error)
	// ProductionConfirmed returns the explicit production confirmation evidence
	// presented for production-classified assets.
	ProductionConfirmed(context.Context, Trigger, string) (bool, error)
}

// Connector authorizes and dials disconnected scope assets for cron runs.
// Every connect requires an explicit opt-in and passes guard.AuthorizeConnect;
// unknown or changed host keys are never auto-accepted.
type Connector struct {
	// OptIn reports whether the run's owner explicitly opted into connecting
	// disconnected assets. Nil or false declines with guard.ErrConnectOptInRequired.
	OptIn func(context.Context, Trigger) (bool, error)
	// Evidence resolves the authorization evidence for the asset.
	Evidence ConnectEvidence
	// Dial performs the actual connect and returns the connected session ID;
	// it must respect ctx cancellation and never auto-accept unknown host keys.
	Dial func(context.Context, Trigger, string) (string, error)
	// Settings provides the per-owner production environment configuration.
	Settings guard.Settings
}

// Connect evaluates the opt-in and every authorization gate, then dials the
// asset. All guard gate failures are returned as their sentinel errors.
func (c *Connector) Connect(ctx context.Context, trigger Trigger, assetID string) (string, error) {
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return "", ErrConnectAssetRequired
	}
	optIn := false
	if c.OptIn != nil {
		var err error
		optIn, err = c.OptIn(ctx, trigger)
		if err != nil {
			return "", fmt.Errorf("cron: evaluate connect opt-in: %w", err)
		}
	}
	if !optIn {
		return "", guard.ErrConnectOptInRequired
	}
	if c.Evidence == nil {
		return "", errors.New("cron: connect evidence is not configured")
	}
	if c.Dial == nil {
		return "", errors.New("cron: connect dialer is not configured")
	}
	ownerMatch, err := c.Evidence.OwnerMatch(ctx, trigger, assetID)
	if err != nil {
		return "", fmt.Errorf("cron: resolve connect owner: %w", err)
	}
	hostKey, err := c.Evidence.HostKey(ctx, trigger, assetID)
	if err != nil {
		return "", fmt.Errorf("cron: resolve connect host key: %w", err)
	}
	vaultReady, err := c.Evidence.VaultReady(ctx, trigger, assetID)
	if err != nil {
		return "", fmt.Errorf("cron: resolve connect vault state: %w", err)
	}
	environment, err := c.Evidence.Environment(ctx, trigger, assetID)
	if err != nil {
		return "", fmt.Errorf("cron: resolve connect environment: %w", err)
	}
	productionConfirmed, err := c.Evidence.ProductionConfirmed(ctx, trigger, assetID)
	if err != nil {
		return "", fmt.Errorf("cron: resolve connect production confirmation: %w", err)
	}
	if err := guard.AuthorizeConnect(ctx, c.Settings, guard.ConnectAuthorization{
		OptIn: optIn, OwnerMatch: ownerMatch, HostKey: hostKey, VaultReady: vaultReady,
		Environment: environment, ProductionConfirmed: productionConfirmed,
	}); err != nil {
		return "", err
	}
	sessionID, err := c.Dial(ctx, trigger, assetID)
	if err != nil {
		return "", fmt.Errorf("cron: connect asset %s: %w", assetID, err)
	}
	if strings.TrimSpace(sessionID) == "" {
		return "", fmt.Errorf("cron: connect asset %s: empty session ID", assetID)
	}
	return sessionID, nil
}
