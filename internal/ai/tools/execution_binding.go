package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var errExecutionTargetChanged = errors.New("执行目标、资产身份或运行实例在授权后已变化，请重新评估")

func (r *Registry) bindExecutionTarget(ctx context.Context, jobID string, scope Scope, call *Call) error {
	var current *TargetBinding
	switch call.Name {
	case "send_keys":
		var args sendKeysArgs
		if err := decode(call.Args, &args); err != nil {
			return err
		}
		terminal, ok := r.deps.Terminal.(TabExecutionIdentityTerminal)
		if !ok {
			if call.Target != nil {
				return errExecutionTargetChanged
			}
			return nil
		}
		tabID, err := r.resolveTab(ctx, jobID, scope, args.TabID)
		if err != nil {
			return err
		}
		sessionID, episode, asset, err := terminal.TabExecutionIdentity(ctx, tabID)
		if err != nil {
			return err
		}
		if sessionID == "" || episode == "" {
			return ErrAssetIdentityUnverifiable
		}
		current = &TargetBinding{Kind: "terminal_tab", SessionID: sessionID, TabID: tabID, RuntimeEpisode: episode, Asset: asset}
	case "exec_commands":
		var args ExecCommandsArgs
		if err := decode(call.Args, &args); err != nil {
			return err
		}
		if len(args.AssetIDs) != 0 || len(args.Tags) != 0 || scope.SessionID == "" {
			if call.Target != nil {
				return errExecutionTargetChanged
			}
			return nil
		}
		resolver, ok := r.deps.Terminal.(SessionAssetIdentityResolver)
		if !ok {
			if call.Target != nil {
				return errExecutionTargetChanged
			}
			return nil
		}
		asset, err := resolver.SessionAssetIdentity(ctx, scope.SessionID)
		if err != nil {
			return err
		}
		current = &TargetBinding{Kind: "session", SessionID: scope.SessionID, Asset: asset}
	case "db_query":
		connID := strings.TrimSpace(scope.ConnID)
		identity, ok := r.deps.Database.(ConnAssetIdentity)
		if !ok || connID == "" {
			if call.Target != nil {
				return errExecutionTargetChanged
			}
			return nil
		}
		assetID, _ := identity.AssetIDOf(ctx, connID)
		current = &TargetBinding{Kind: "database", ConnID: connID, Asset: AssetIdentity{AssetID: assetID}}
	default:
		if call.Target != nil {
			return errExecutionTargetChanged
		}
		return nil
	}
	if call.Target != nil && *call.Target != *current {
		return fmt.Errorf("%w: 原绑定=%+v 当前=%+v", errExecutionTargetChanged, *call.Target, *current)
	}
	call.Target = current
	return nil
}

func (r *Registry) revalidateExecutionAuthorization(ctx context.Context, call Call) error {
	if call.Authorization == nil || call.OneTimeConfirmation {
		return nil
	}
	if r.deps.Grants == nil {
		return errors.New("自动授权管理未配置，无法复核执行准入")
	}
	if call.Target != nil && call.Target.Asset.AssetID != call.Authorization.DeviceID {
		return errExecutionTargetChanged
	}
	return r.deps.Grants.RevalidateAuthorization(ctx, call.Authorization)
}

func outcomeAuthorizationID(call Call) string {
	if call.Target == nil && call.Authorization == nil {
		return call.AuthorizationID
	}
	payload := struct {
		AuthorizationID string
		Authorization   any
		Target          *TargetBinding
	}{AuthorizationID: call.AuthorizationID, Authorization: call.Authorization, Target: call.Target}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return call.AuthorizationID + "|execution:" + hex.EncodeToString(sum[:])
}
