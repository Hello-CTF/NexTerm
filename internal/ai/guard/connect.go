package guard

import (
	"context"
	"errors"
)

// HostKeyState is the known-host verdict for a connect target.
type HostKeyState string

const (
	HostKeyKnown   HostKeyState = "known"
	HostKeyUnknown HostKeyState = "unknown"
	HostKeyChanged HostKeyState = "changed"
)

var (
	// ErrConnectOptInRequired rejects connects of disconnected assets without an explicit opt-in.
	ErrConnectOptInRequired = errors.New("未在「生产安全 → 自动连接」中允许自动连接该主机")
	// ErrConnectOwnerMismatch rejects connects to assets owned by another identity.
	ErrConnectOwnerMismatch = errors.New("资产不属于当前身份")
	// ErrConnectHostKeyUnknown rejects targets whose host key is not in the known-host database;
	// unknown host keys are never auto-accepted, not even with an explicit opt-in.
	ErrConnectHostKeyUnknown = errors.New("未知 host key 不会被自动接受")
	// ErrConnectHostKeyChanged rejects targets whose host key changed; the interactive
	// host-key confirmation flow must resolve the conflict first.
	ErrConnectHostKeyChanged = errors.New("host key 已变更，必须人工确认")
	// ErrConnectVaultUnavailable rejects targets whose credential is unavailable.
	ErrConnectVaultUnavailable = errors.New("目标资产凭据不可用")
	// ErrConnectProductionConfirmationRequired rejects connects to production assets without
	// the explicit production confirmation evidence.
	ErrConnectProductionConfirmationRequired = errors.New("未在「生产安全 → 自动连接」中允许自动连接生产环境主机")
)

// ConnectAuthorization carries the evidence evaluated before an opt-in connect of a
// disconnected asset for cron jobs, broadcasts, or target sets. The zero value denies.
type ConnectAuthorization struct {
	// OptIn is the explicit user opt-in for connecting disconnected assets.
	OptIn bool
	// OwnerMatch reports whether the resolved asset owner matches the requesting identity.
	OwnerMatch bool
	// HostKey is the known-host verdict for the target.
	HostKey HostKeyState
	// VaultReady reports whether the target's credential is available.
	VaultReady bool
	// Environment is the resolved environment name of the target asset.
	Environment string
	// ProductionConfirmed is the explicit production confirmation evidence presented
	// for production-classified targets.
	ProductionConfirmed bool
}

// AuthorizeConnect decides whether an opt-in connect of a disconnected asset is
// allowed: every gate must pass, and unknown or changed host keys are never
// auto-accepted. Production classification honors the owner's configured list.
func AuthorizeConnect(ctx context.Context, settings Settings, auth ConnectAuthorization) error {
	if !auth.OptIn {
		return ErrConnectOptInRequired
	}
	if !auth.OwnerMatch {
		return ErrConnectOwnerMismatch
	}
	switch auth.HostKey {
	case HostKeyKnown:
	case HostKeyChanged:
		return ErrConnectHostKeyChanged
	default:
		return ErrConnectHostKeyUnknown
	}
	if !auth.VaultReady {
		return ErrConnectVaultUnavailable
	}
	production, err := IsProductionEnvironment(ctx, settings, auth.Environment)
	if err != nil {
		return err
	}
	if production && !auth.ProductionConfirmed {
		return ErrConnectProductionConfirmationRequired
	}
	return nil
}
