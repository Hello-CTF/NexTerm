package agent

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

// Permission sources reported to the panel: the global mode, or an explicit
// override stored for the scope's host.
const (
	PermissionSourceGlobal = "global"
	PermissionSourceHost   = "host"
)

// PermissionState describes the permission that applies to a scope and why.
type PermissionState struct {
	// Config is the effective configuration: the global one with the host's
	// explicit mode applied.
	Config     guard.Config `json:"config"`
	GlobalMode guard.Mode   `json:"globalMode"`
	// HostMode is the explicit override stored for the scope's host, if any.
	HostMode guard.Mode `json:"hostMode,omitempty"`
	// HostResolved reports that the scope names a host, so an override can be set for it.
	HostResolved bool   `json:"hostResolved"`
	Source       string `json:"source"`
}

// effectivePermission is the permission a run applies: the stored global
// configuration with the explicit mode of the scope's host. It is read again
// at every tool call, so a change in the panel applies to a running job.
func (r *Runner) effectivePermission(ctx context.Context, scope tools.Scope) (guard.Config, error) {
	permission, err := r.config.Permission(ctx)
	if err != nil {
		return guard.Config{}, err
	}
	permission = permission.Normalized()
	if r.config.Permissions == nil {
		return permission, nil
	}
	return r.config.Permissions.WithDeviceMode(ctx, permission, tools.ResolveGrantDeviceID(ctx, r.config.Tools, scope))
}

func (r *Runner) permissionState(ctx context.Context, scope tools.Scope) (PermissionState, error) {
	global, err := r.config.Permissions.Get(ctx)
	if err != nil {
		return PermissionState{}, err
	}
	state := PermissionState{Config: global, GlobalMode: global.Mode, Source: PermissionSourceGlobal}
	deviceID := tools.ResolveGrantDeviceID(ctx, r.config.Tools, scope)
	if deviceID == "" {
		return state, nil
	}
	state.HostResolved = true
	mode, ok, err := r.config.Permissions.DeviceMode(ctx, deviceID)
	if err != nil {
		return PermissionState{}, err
	}
	if !ok {
		return state, nil
	}
	state.Config, err = r.config.Permissions.WithDeviceMode(ctx, global, deviceID)
	if err != nil {
		return PermissionState{}, err
	}
	state.HostMode = mode
	state.Source = PermissionSourceHost
	return state, nil
}
