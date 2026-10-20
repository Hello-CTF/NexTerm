package production

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	fleetinventory "github.com/Hello-CTF/NexTerm/internal/fleet/inventory"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/sshconfig"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const (
	inventoryDiscoveryTimeout  = 15 * time.Second
	inventoryTargetExecTimeout = 30 * time.Second
)

type inventoryDiscoverRequest struct {
	Source string `json:"source"`
	Path   string `json:"path,omitempty"`
}

type inventorySyncRequest struct {
	Source  string    `json:"source"`
	Path    string    `json:"path,omitempty"`
	GroupID string    `json:"groupId,omitempty"`
	Keys    *[]string `json:"keys,omitempty"`
}

type inventoryCandidateDTO struct {
	Key      string         `json:"key"`
	Kind     string         `json:"kind"`
	Name     string         `json:"name"`
	Host     *string        `json:"host"`
	Port     *int32         `json:"port"`
	Username *string        `json:"username"`
	AuthKind *string        `json:"authKind"`
	KeyPath  *string        `json:"keyPath"`
	CredID   *string        `json:"credId"`
	Options  map[string]any `json:"options"`
}

type inventoryDiscoveryDTO struct {
	Source   string                  `json:"source"`
	Path     string                  `json:"path,omitempty"`
	Items    []inventoryCandidateDTO `json:"items"`
	Warnings []string                `json:"warnings"`
}

type inventorySyncDTO struct {
	GroupID   string   `json:"groupId"`
	Created   int      `json:"created"`
	Updated   int      `json:"updated"`
	Removed   int      `json:"removed"`
	Unchanged int      `json:"unchanged"`
	Warnings  []string `json:"warnings"`
}

type inventorySSHWatchRequest struct {
	Enabled *bool  `json:"enabled,omitempty"`
	Path    string `json:"path,omitempty"`
	GroupID string `json:"groupId,omitempty"`
}

type inventorySSHWatchDTO struct {
	Watching   bool              `json:"watching"`
	Path       string            `json:"path"`
	GroupID    string            `json:"groupId,omitempty"`
	LastSyncAt *int64            `json:"lastSyncAt"`
	LastResult *inventorySyncDTO `json:"lastResult"`
	LastError  string            `json:"lastError"`
}

type inventoryTargetExecRequest struct {
	Kind   string         `json:"kind"`
	Target map[string]any `json:"target"`
	Action []string       `json:"action"`
}

type inventoryTargetExecResponse struct {
	Output string `json:"output"`
}

func inventorySyncDTOFromStore(result store.InventorySyncResult, warnings []string) inventorySyncDTO {
	if warnings == nil {
		warnings = []string{}
	}
	return inventorySyncDTO{
		GroupID: result.GroupID, Created: result.Created, Updated: result.Updated,
		Removed: result.Removed, Unchanged: result.Unchanged, Warnings: warnings,
	}
}

func inventoryCandidateDTOFromStore(item store.InventoryItem) inventoryCandidateDTO {
	options := item.Options
	if options == nil {
		options = map[string]any{}
	}
	return inventoryCandidateDTO{
		Key: item.Key, Kind: item.Kind, Name: item.Name, Host: item.Host, Port: item.Port,
		Username: item.Username, AuthKind: item.AuthKind, KeyPath: item.KeyPath, CredID: item.CredID, Options: options,
	}
}

func effectiveInventorySSHPath(path string) string {
	if trimmed := strings.TrimSpace(path); trimmed != "" {
		return trimmed
	}
	return sshconfig.DefaultConfigPath()
}

func guardInventoryServerPath(path string, desktop bool) error {
	if desktop {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ipc.NewError(ipc.CodeUnsupported, "服务端模式下无法定位当前用户主目录")
	}
	rel, err := filepath.Rel(filepath.Join(home, ".ssh"), path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("服务端模式下只能读取当前用户 .ssh 目录内的清单：%s", path))
	}
	return nil
}

func inventoryItemsFromFleet(candidates []fleetinventory.Candidate) []store.InventoryItem {
	items := make([]store.InventoryItem, len(candidates))
	for index, candidate := range candidates {
		items[index] = store.InventoryItem{
			Key: candidate.Key, Kind: candidate.Kind, Name: candidate.Name, Host: candidate.Host, Port: candidate.Port,
			Username: candidate.Username, AuthKind: candidate.AuthKind, KeyPath: candidate.KeyPath, CredID: candidate.CredID, Options: candidate.Options,
		}
	}
	return items
}

func (s *inventoryService) sshConfigItems(ctx context.Context, path string) ([]store.InventoryItem, []string, error) {
	if path == "" {
		return nil, nil, ipc.NewError(ipc.CodeBadParam, "找不到默认 SSH 配置文件路径，请手动指定")
	}
	if err := guardInventoryServerPath(path, s.desktop); err != nil {
		return nil, nil, err
	}
	limits := s.sshLimits
	if !s.desktop {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			limits.RootDir = filepath.Join(home, ".ssh")
		}
	}
	parsed, err := sshconfig.Parse(path, limits)
	if err != nil {
		if os.IsNotExist(err) {
			return []store.InventoryItem{}, []string{}, nil
		}
		return nil, nil, ipc.NewError(ipc.CodeBadParam, "解析 SSH 配置失败: "+err.Error())
	}
	if parsed.Truncated {
		return nil, nil, ipc.NewError(ipc.CodeBadParam, "SSH 配置快照不完整（超出解析上限），已保留上次有效清单")
	}
	for _, diagnostic := range parsed.Diagnostics {
		switch diagnostic.Code {
		case "include-depth-exceeded", "file-too-large", "include-out-of-bounds":
			return nil, nil, ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("SSH 配置快照不完整（%s: %s），已保留上次有效清单", diagnostic.Code, diagnostic.Source))
		}
	}
	assets, err := s.database.AssetList(ctx, false)
	if err != nil {
		return nil, nil, err
	}
	assetIDByName := map[string]string{}
	for _, asset := range assets {
		if asset.Kind != "ssh" && asset.Kind != "docker" {
			continue
		}
		name := strings.ToLower(asset.Name)
		if _, exists := assetIDByName[name]; !exists {
			assetIDByName[name] = asset.ID
		}
	}
	items := make([]store.InventoryItem, 0, len(parsed.Hosts))
	warnings := make([]string, 0, len(parsed.Diagnostics))
	for _, diagnostic := range parsed.Diagnostics {
		warnings = append(warnings, fmt.Sprintf("%s: %s", diagnostic.Source, diagnostic.Message))
	}
	for _, host := range parsed.Hosts {
		port := int32(host.Port)
		username := host.Username
		hostname := host.Hostname
		authKind := "agent"
		var keyPath *string
		if len(host.IdentityFiles) > 0 {
			authKind = "key"
			keyPath = &host.IdentityFiles[0]
		}
		target := map[string]any{
			"type": "ssh-config", "alias": host.Alias, "proxyJump": host.ProxyJump,
			"identityFiles": append([]string(nil), host.IdentityFiles...), "sourcePath": host.Source,
		}
		options := map[string]any{"inventoryTarget": target}
		hops := sshImportJumpHops(host.ProxyJump)
		switch {
		case len(hops) == 1:
			if jumpID := assetIDByName[strings.ToLower(hops[0])]; jumpID != "" {
				options["jumpAssetId"] = jumpID
			} else {
				warnings = append(warnings, fmt.Sprintf("主机 %q 的跳板 %q 未能解析，已保留源记录但未设置跳板", host.Alias, hops[0]))
			}
		case len(hops) > 1:
			warnings = append(warnings, fmt.Sprintf("主机 %q 使用多跳 ProxyJump，当前动态清单不映射跳板", host.Alias))
		}
		items = append(items, store.InventoryItem{
			Key: "host/" + host.Alias, Kind: "ssh", Name: host.Alias,
			Host: &hostname, Port: &port, Username: optionalString(username), AuthKind: &authKind, KeyPath: keyPath, Options: options,
		})
	}
	return items, warnings, nil
}

func (s *inventoryService) discover(ctx context.Context, source, path string) ([]store.InventoryItem, []string, error) {
	switch source {
	case store.InventorySourceSSHConfig:
		return s.sshConfigItems(ctx, effectiveInventorySSHPath(path))
	case store.InventorySourceDockerContext, store.InventorySourceKubernetes:
		discoverCtx, cancel := context.WithTimeout(ctx, inventoryDiscoveryTimeout)
		defer cancel()
		var candidates []fleetinventory.Candidate
		var err error
		if source == store.InventorySourceDockerContext {
			candidates, err = fleetinventory.DiscoverDockerContexts(discoverCtx, s.runner)
		} else {
			candidates, err = fleetinventory.DiscoverKubernetesGated(discoverCtx, s.runner, s.kubeconfigGate)
		}
		if err != nil {
			if errors.Is(err, fleetinventory.ErrKubeconfigNotAuthorized) {
				return nil, nil, ipc.NewError(ipc.CodeForbidden, err.Error())
			}
			return nil, nil, ipc.NewError(ipc.CodeBadParam, err.Error())
		}
		return inventoryItemsFromFleet(candidates), []string{}, nil
	default:
		return nil, nil, ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("未知的清单来源 %q", source))
	}
}

func inventorySelectCandidates(source string, items []store.InventoryItem, keys *[]string) ([]store.InventoryItem, error) {
	if source == store.InventorySourceSSHConfig {
		return items, nil
	}
	if keys == nil {
		return nil, ipc.NewError(ipc.CodeBadParam, "Docker 和 Kubernetes 导入必须显式提供候选项 keys")
	}
	selected := make(map[string]bool, len(*keys))
	for _, key := range *keys {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, ipc.NewError(ipc.CodeBadParam, "候选项 key 不能为空")
		}
		if selected[key] {
			return nil, ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("候选项 key %q 重复", key))
		}
		selected[key] = true
	}
	byKey := make(map[string]store.InventoryItem, len(items))
	for _, item := range items {
		byKey[item.Key] = item
	}
	for key := range selected {
		if _, exists := byKey[key]; !exists {
			return nil, ipc.NewError(ipc.CodeBadParam, "清单预览已变化，请重新发现后再同步")
		}
	}
	result := make([]store.InventoryItem, 0, len(selected))
	for _, item := range items {
		if selected[item.Key] {
			result = append(result, item)
		}
	}
	return result, nil
}

func inventoryResultChanged(result store.InventorySyncResult) bool {
	return result.Created+result.Updated+result.Removed > 0
}

func (s *inventoryService) emitChanged(ctx context.Context, source string, result store.InventorySyncResult) {
	if !inventoryResultChanged(result) {
		return
	}
	_ = ipc.Emit(ctx, s.events, ipc.TopicInventoryChanged, ipc.InventoryChangedEvent{Source: source, GroupID: result.GroupID})
}

func (s *inventoryService) syncSSH(ctx context.Context, path, groupID string) (store.InventorySyncResult, []string, error) {
	var combined store.InventorySyncResult
	var warnings []string
	for pass := 0; pass < 2; pass++ {
		items, discoveredWarnings, err := s.discover(ctx, store.InventorySourceSSHConfig, path)
		if err != nil {
			return store.InventorySyncResult{}, nil, err
		}
		result, err := s.database.InventorySync(ctx, store.InventorySourceSSHConfig, groupID, items)
		if err != nil {
			return store.InventorySyncResult{}, nil, err
		}
		warnings = discoveredWarnings
		if pass == 0 {
			combined = result
		} else {
			combined.Created += result.Created
			combined.Updated += result.Updated
			combined.Removed += result.Removed
			combined.Unchanged = result.Unchanged
		}
		combined.GroupID = result.GroupID
		groupID = result.GroupID
	}
	return combined, warnings, nil
}

func (s *inventoryService) sync(ctx context.Context, input inventorySyncRequest) (inventorySyncDTO, error) {
	if input.Source == store.InventorySourceSSHConfig {
		result, warnings, err := s.syncSSH(ctx, input.Path, input.GroupID)
		if err != nil {
			return inventorySyncDTO{}, err
		}
		s.emitChanged(ctx, input.Source, result)
		return inventorySyncDTOFromStore(result, warnings), nil
	}
	items, warnings, err := s.discover(ctx, input.Source, input.Path)
	if err != nil {
		return inventorySyncDTO{}, err
	}
	selected, err := inventorySelectCandidates(input.Source, items, input.Keys)
	if err != nil {
		return inventorySyncDTO{}, err
	}
	result, err := s.database.InventorySync(ctx, input.Source, input.GroupID, selected)
	if err != nil {
		return inventorySyncDTO{}, err
	}
	s.emitChanged(ctx, input.Source, result)
	return inventorySyncDTOFromStore(result, warnings), nil
}

func (s *inventoryService) execTarget(ctx context.Context, input inventoryTargetExecRequest) (inventoryTargetExecResponse, error) {
	candidate := fleetinventory.Candidate{Kind: input.Kind, Options: map[string]any{"inventoryTarget": input.Target}}
	target, err := fleetinventory.ResolveTarget(candidate)
	if err != nil {
		return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeBadParam, err.Error())
	}
	execCtx, cancel := context.WithTimeout(ctx, inventoryTargetExecTimeout)
	defer cancel()
	output, err := fleetinventory.RunTargetGated(execCtx, s.runner, s.kubeconfigGate, target, input.Action...)
	if err != nil {
		switch {
		case errors.Is(err, fleetinventory.ErrKubeconfigNotAuthorized):
			return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeForbidden, err.Error())
		case errors.Is(err, fleetinventory.ErrEmptyAction),
			errors.Is(err, fleetinventory.ErrNotExecutable),
			errors.Is(err, fleetinventory.ErrInvalidTarget):
			return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeBadParam, err.Error())
		default:
			return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeIO, err.Error())
		}
	}
	return inventoryTargetExecResponse{Output: string(output)}, nil
}

func (s *inventoryService) registerCommands(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.RegisterNested(dispatcher, "inventory_discover", func(ctx context.Context, _ *ipc.Call, input inventoryDiscoverRequest) (inventoryDiscoveryDTO, error) {
				items, warnings, err := s.discover(ctx, input.Source, input.Path)
				if err != nil {
					return inventoryDiscoveryDTO{}, err
				}
				result := inventoryDiscoveryDTO{Source: input.Source, Items: make([]inventoryCandidateDTO, len(items)), Warnings: warnings}
				if input.Source == store.InventorySourceSSHConfig {
					result.Path = effectiveInventorySSHPath(input.Path)
				}
				for index, item := range items {
					result.Items[index] = inventoryCandidateDTOFromStore(item)
				}
				return result, nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "inventory_sync", func(ctx context.Context, _ *ipc.Call, input inventorySyncRequest) (inventorySyncDTO, error) {
				return s.sync(ctx, input)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "inventory_ssh_watch", func(ctx context.Context, _ *ipc.Call, input inventorySSHWatchRequest) (inventorySSHWatchDTO, error) {
				if input.Enabled == nil {
					return s.watchStatus(ctx), nil
				}
				if !*input.Enabled {
					return inventorySSHWatchDTO{}, s.stopWatch(ctx, ipc.OwnerID(ctx))
				}
				return s.startWatch(ctx, input.Path, input.GroupID)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "inventory_target_exec", func(ctx context.Context, _ *ipc.Call, input inventoryTargetExecRequest) (inventoryTargetExecResponse, error) {
				return s.execTarget(ctx, input)
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}
