package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/docker"
	fleetinventory "github.com/Hello-CTF/NexTerm/internal/fleet/inventory"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
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
	Kind      string         `json:"kind"`
	Target    map[string]any `json:"target"`
	Action    string         `json:"action"`
	Container string         `json:"container,omitempty"`
	Command   []string       `json:"command"`
	// SessionID selects the caller's managed session to the Docker host for
	// server-side docker-context exec; the context endpoint must map to the
	// session's asset.
	SessionID string `json:"sessionId,omitempty"`
	// OperationID is the caller-generated unique identity of one exec
	// launch; the cancel action must echo it to address exactly that
	// instance. It is required, non-empty, and length-bounded.
	OperationID string `json:"operationId"`
	// Confirmation carries the production confirmation hashes when the
	// session asset is tagged as a production environment.
	Confirmation *dockerProductionConfirmation `json:"confirmation,omitempty"`
}

type inventoryTargetExecResponse struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exitCode"`
}

// inventoryTargetExecFailure details a command that ran but exited non-zero;
// the output is preserved so callers can inspect the failed command's result.
type inventoryTargetExecFailure struct {
	ExitCode int    `json:"exitCode"`
	Output   string `json:"output"`
}

// inventoryTargetActionExec is the only target action the IPC exposes;
// free-form Docker or kubectl argv is deliberately not representable.
const (
	inventoryTargetActionExec     = "exec"
	inventoryTargetActionCancel   = "cancel"
	inventoryOperationIDMaxLength = 128
	inventoryExecTombstoneLimit   = 1000
)

func validateInventoryOperationID(operationID string) error {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return errors.New("operationId 不能为空")
	}
	if len(operationID) > inventoryOperationIDMaxLength {
		return fmt.Errorf("operationId 长度不能超过 %d", inventoryOperationIDMaxLength)
	}
	return nil
}

// inventoryTargetKey returns the canonical identity of a target. For a
// Kubernetes pod it includes the context, namespace, pod, and the resolved
// effective container (override or the target's default), so the same
// namespace/pod across contexts or with different default containers never
// collides.
func inventoryTargetKey(kind string, target fleetinventory.Target, container string) string {
	switch {
	case target.Docker != nil:
		return "context/" + target.Docker.Name
	case target.Kubernetes != nil:
		kubernetes := target.Kubernetes
		if kubernetes.Pod != "" {
			effective := strings.TrimSpace(container)
			if effective == "" {
				effective = kubernetes.Container
			}
			return kubernetes.Context + "/" + kubernetes.Namespace + "/" + kubernetes.Pod + "/" + effective
		}
		return "context/" + kubernetes.Context
	default:
		return kind
	}
}

// registerExec binds an in-flight exec to its {owner, operationId}
// namespace. Starting an operationId that is already in flight for that
// owner is rejected, and a tombstone from an earlier cancel blocks the
// operation from starting at all.
func (s *inventoryService) registerExec(key inventoryExecKey, handle *inventoryExecHandle) error {
	s.execMu.Lock()
	defer s.execMu.Unlock()
	if _, tombstoned := s.execTombstones[key]; tombstoned {
		return ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("operation %q 已被取消，不能重复启动", key.operationID))
	}
	if _, exists := s.execCancels[key]; exists {
		return ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("operationId %q 已在使用中", key.operationID))
	}
	s.execCancels[key] = handle
	return nil
}

func (s *inventoryService) unregisterExec(key inventoryExecKey, handle *inventoryExecHandle) {
	s.execMu.Lock()
	defer s.execMu.Unlock()
	if s.execCancels[key] == handle {
		delete(s.execCancels, key)
	}
}

// addExecTombstone records a cancelled operationId under its owner's
// namespace. Existing tombstones are not re-queued, so a duplicate cancel
// can never evict the protection it just wrote, and each owner's capacity
// is bounded independently so one owner's flood cannot evict another's
// protection.
func (s *inventoryService) addExecTombstone(key inventoryExecKey) {
	if _, exists := s.execTombstones[key]; exists {
		return
	}
	s.execTombstones[key] = struct{}{}
	s.execTombstoneFIFO = append(s.execTombstoneFIFO, key)
	ownerCount := 0
	for _, entry := range s.execTombstoneFIFO {
		if entry.ownerID == key.ownerID {
			ownerCount++
		}
	}
	if ownerCount > inventoryExecTombstoneLimit {
		for index, entry := range s.execTombstoneFIFO {
			if entry.ownerID == key.ownerID {
				delete(s.execTombstones, entry)
				s.execTombstoneFIFO = append(s.execTombstoneFIFO[:index], s.execTombstoneFIFO[index+1:]...)
				break
			}
		}
	}
}

// cancelTargetExec cancels exactly the in-flight exec identified by the
// caller's namespace and the request's operationId. A cancel whose handle
// is gone (settled or never started) leaves a tombstone so the operation
// cannot start later; a cancel whose launch metadata does not match the
// handle is a no-op and never tombstones the legitimate instance.
func (s *inventoryService) cancelTargetExec(ctx context.Context, input inventoryTargetExecRequest) (inventoryTargetExecResponse, error) {
	operationID := strings.TrimSpace(input.OperationID)
	if err := validateInventoryOperationID(operationID); err != nil {
		return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeBadParam, err.Error())
	}
	key := inventoryExecKey{ownerID: inventoryOwnerID(ctx), operationID: operationID}
	targetKey := ""
	candidate := fleetinventory.Candidate{Kind: input.Kind, Options: map[string]any{"inventoryTarget": input.Target}}
	if target, err := fleetinventory.ResolveTarget(candidate); err == nil {
		targetKey = inventoryTargetKey(input.Kind, target, input.Container)
	}
	s.execMu.Lock()
	handle := s.execCancels[key]
	if handle != nil && (handle.kind != input.Kind || handle.targetKey != targetKey ||
		handle.container != strings.TrimSpace(input.Container) || handle.sessionID != strings.TrimSpace(input.SessionID)) {
		handle = nil
	}
	if handle != nil {
		handle.cancel()
		delete(s.execCancels, key)
		s.addExecTombstone(key)
	} else if _, exists := s.execCancels[key]; !exists {
		s.addExecTombstone(key)
	}
	s.execMu.Unlock()
	return inventoryTargetExecResponse{}, nil
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
	operationID := strings.TrimSpace(input.OperationID)
	if err := validateInventoryOperationID(operationID); err != nil {
		return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeBadParam, err.Error())
	}
	if input.Action == inventoryTargetActionCancel {
		return s.cancelTargetExec(ctx, input)
	}
	if input.Action != inventoryTargetActionExec {
		return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("不支持的目标动作 %q，仅支持 %q", input.Action, inventoryTargetActionExec))
	}
	candidate := fleetinventory.Candidate{Kind: input.Kind, Options: map[string]any{"inventoryTarget": input.Target}}
	target, err := fleetinventory.ResolveTarget(candidate)
	if err != nil {
		return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeBadParam, err.Error())
	}
	if !s.desktop && target.Docker != nil {
		return s.execDockerTargetViaSession(ctx, input, target)
	}
	execCtx, cancel := context.WithTimeout(ctx, inventoryTargetExecTimeout)
	defer cancel()
	handle := &inventoryExecHandle{
		cancel:    cancel,
		kind:      input.Kind,
		targetKey: inventoryTargetKey(input.Kind, target, input.Container),
		container: strings.TrimSpace(input.Container),
		sessionID: strings.TrimSpace(input.SessionID),
	}
	key := inventoryExecKey{ownerID: inventoryOwnerID(ctx), operationID: operationID}
	if err := s.registerExec(key, handle); err != nil {
		return inventoryTargetExecResponse{}, err
	}
	defer s.unregisterExec(key, handle)
	output, execErr := fleetinventory.RunTargetGated(execCtx, s.runner, s.kubeconfigGate, target, fleetinventory.TargetExecAction{
		Container: input.Container, Command: input.Command,
	})
	if execErr != nil {
		if errors.Is(execCtx.Err(), context.Canceled) {
			s.auditTargetExec(ctx, input, target, "", execErr)
			return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeIO, "执行已取消").WithDetail(map[string]any{"cancelled": true})
		}
		var exitErr *fleetinventory.CommandExitError
		if errors.As(execErr, &exitErr) {
			s.auditTargetExec(ctx, input, target, "", execErr)
			return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeIO, "命令已执行但返回非零退出码").
				WithDetail(inventoryTargetExecFailure{ExitCode: exitErr.Code, Output: string(exitErr.Output)})
		}
		s.auditTargetExec(ctx, input, target, "", execErr)
		return inventoryTargetExecResponse{}, inventoryTargetExecError(execErr)
	}
	s.auditTargetExec(ctx, input, target, "", nil)
	return inventoryTargetExecResponse{Output: string(output)}, nil
}

// execDockerTargetViaSession runs the exec through the caller's own managed
// session and its Docker backend. Inventory import records are target
// resolution only: without an owner-scoped session whose asset matches the
// context endpoint, or without an available backend, the exec fails closed.
func (s *inventoryService) execDockerTargetViaSession(ctx context.Context, input inventoryTargetExecRequest, target fleetinventory.Target) (inventoryTargetExecResponse, error) {
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeBadParam, "服务端 Docker target exec 需要指定受管会话 sessionId")
	}
	if s.sessions == nil || s.docker == nil {
		return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeUnsupported, "服务端 Docker target exec 不可用：缺少会话或 Docker 后端")
	}
	connected, err := s.sessions.SessionFor(productionSessionRequestContext(ctx), sessionID)
	if err != nil {
		return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeForbidden, "受管会话不存在或不属于当前账号")
	}
	container := strings.TrimSpace(input.Container)
	if container == "" {
		return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeBadParam, fleetinventory.ErrInvalidTarget.Error()+": docker exec 需要指定容器")
	}
	if strings.HasPrefix(container, "-") {
		return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeBadParam, fleetinventory.ErrInvalidTarget.Error()+": 容器名不能以 flag 前缀开头")
	}
	if len(input.Command) == 0 {
		return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeBadParam, fleetinventory.ErrEmptyAction.Error())
	}
	assetRow, err := s.database.AssetGetLive(productionSessionRequestContext(ctx), connected.Info().AssetID)
	if err != nil {
		return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeForbidden, "受管会话资产不可用")
	}
	// The endpoint check must bind the actual connection target: when the
	// live asset's config fingerprint diverges from the session snapshot,
	// the session still points at the old host and must be reconnected.
	if productionAssetConfigFingerprint(assetRow) != connected.Asset().ConfigFingerprint {
		return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeForbidden, "受管会话配置已过期，请重新连接后重试")
	}
	endpoint, err := fleetinventory.ParseDockerEndpoint(target.Docker.Endpoint)
	if err != nil || !dockerEndpointMatchesAsset(endpoint, assetRow) {
		return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeForbidden, fmt.Sprintf("Docker context %q 的端点与受管会话资产不匹配", target.Docker.Name))
	}
	if err := s.confirmInventoryDockerExec(ctx, connected, assetRow, target.Docker.Name, container, input.Command, input.Confirmation); err != nil {
		return inventoryTargetExecResponse{}, err
	}
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	handle := &inventoryExecHandle{
		cancel:    cancel,
		kind:      input.Kind,
		targetKey: inventoryTargetKey(input.Kind, target, container),
		container: container,
		sessionID: sessionID,
	}
	key := inventoryExecKey{ownerID: inventoryOwnerID(ctx), operationID: strings.TrimSpace(input.OperationID)}
	if err := s.registerExec(key, handle); err != nil {
		return inventoryTargetExecResponse{}, err
	}
	defer s.unregisterExec(key, handle)
	result, execErr := s.docker.Exec(execCtx, docker.ExecRequest{
		SessionID: sessionID, Container: container, Command: inventoryShellJoin(input.Command),
	})
	if execErr != nil {
		if errors.Is(execCtx.Err(), context.Canceled) {
			s.auditTargetExec(ctx, input, target, sessionID, execErr)
			return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeIO, "执行已取消").WithDetail(map[string]any{"cancelled": true})
		}
		s.auditTargetExec(ctx, input, target, sessionID, execErr)
		return inventoryTargetExecResponse{}, terminalIPCError(execErr)
	}
	if result.ExitCode != 0 {
		execErr = &inventoryExecExitError{code: result.ExitCode}
		s.auditTargetExec(ctx, input, target, sessionID, execErr)
		return inventoryTargetExecResponse{}, ipc.NewError(ipc.CodeIO, "命令已执行但返回非零退出码").
			WithDetail(inventoryTargetExecFailure{ExitCode: result.ExitCode, Output: result.Output})
	}
	s.auditTargetExec(ctx, input, target, sessionID, nil)
	return inventoryTargetExecResponse{Output: result.Output, ExitCode: result.ExitCode}, nil
}

// inventoryExecExitError marks a command that ran but exited non-zero on the
// session-backed docker path.
type inventoryExecExitError struct{ code int }

func (e *inventoryExecExitError) Error() string { return fmt.Sprintf("exit code %d", e.code) }

// dockerEndpointMatchesAsset reports whether the context endpoint addresses
// the session asset's host. The scheme carries the transport semantics: an
// ssh endpoint is matched against the asset's SSH port (default 22), while a
// tcp endpoint is the Docker daemon port and is never compared with the
// session's SSH/WinRM transport port. Local socket endpoints never match.
func dockerEndpointMatchesAsset(endpoint fleetinventory.DockerEndpoint, asset store.AssetRow) bool {
	if endpoint.Host == "" || asset.Host == nil || !strings.EqualFold(endpoint.Host, *asset.Host) {
		return false
	}
	switch endpoint.Scheme {
	case "ssh":
		if endpoint.Username != "" && (asset.Username == nil || *asset.Username != endpoint.Username) {
			return false
		}
		// An ssh endpoint without a port means SSH port 22; normalize both
		// sides to the effective SSH port and compare numerically.
		endpointPort := 22
		if endpoint.Port != "" {
			parsed, err := strconv.Atoi(endpoint.Port)
			if err != nil || parsed <= 0 || parsed > 65535 {
				return false
			}
			endpointPort = parsed
		}
		assetPort := 22
		if asset.Port != nil {
			assetPort = int(*asset.Port)
		}
		return endpointPort == assetPort
	case "tcp":
		return true
	default:
		return false
	}
}

func inventoryShellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for index, arg := range argv {
		parts[index] = "'" + strings.ReplaceAll(arg, "'", `'"'"'`) + "'"
	}
	return strings.Join(parts, " ")
}

func inventoryTargetExecError(execErr error) error {
	switch {
	case errors.Is(execErr, fleetinventory.ErrKubeconfigNotAuthorized):
		return ipc.NewError(ipc.CodeForbidden, execErr.Error())
	case errors.Is(execErr, fleetinventory.ErrEmptyAction),
		errors.Is(execErr, fleetinventory.ErrNotExecutable),
		errors.Is(execErr, fleetinventory.ErrInvalidTarget):
		return ipc.NewError(ipc.CodeBadParam, execErr.Error())
	default:
		return ipc.NewError(ipc.CodeIO, execErr.Error())
	}
}

// confirmInventoryDockerExec reuses the docker production confirmation flow:
// session assets tagged as production require explicit confirmation bound to
// the canonical context, container, and full command argv, and the
// confirmation is audited before the exec runs.
func (s *inventoryService) confirmInventoryDockerExec(ctx context.Context, connected *session.Session, asset store.AssetRow, contextName, container string, command []string, confirmation *dockerProductionConfirmation) error {
	info := connected.Info()
	target := dockerConfirmationTarget{
		SessionID: info.ID, SessionName: info.Name, AssetID: asset.ID,
		Environment: store.AssetEnvironment(asset),
	}
	if asset.Host != nil {
		target.Hostname = *asset.Host
	}
	operation := dockerMutationOperation{
		Kind:        "inventory_target_exec",
		Project:     contextName,
		Path:        container,
		ContentHash: dockerConfirmationDigest("command-v1", canonicalInventoryCommand(command)),
	}
	challenge, err := newDockerProductionChallenge(target, operation, s.classifyProductionEnvironments(ctx))
	if err != nil {
		return err
	}
	if err := validateDockerProductionConfirmation(confirmation, challenge); err != nil {
		return dockerMutationIPCError(err)
	}
	if challenge == nil {
		return nil
	}
	auditCtx, cancel := context.WithTimeout(ipc.WithUserID(context.WithoutCancel(ctx), ipc.OwnerID(ctx)), 5*time.Second)
	defer cancel()
	return s.database.AuditInsert(auditCtx, newDockerConfirmationAuditInput(ctx, target, challenge, operation, time.Now().UnixMilli()))
}

// classifyProductionEnvironments resolves whether an asset environment counts
// as production for the requesting owner: the built-in names plus the owner's
// configured additions. A nil asset store degrades to the built-in names.
func (s *inventoryService) classifyProductionEnvironments(ctx context.Context) productionEnvironmentClassifier {
	return func(environment string) (bool, error) {
		if strings.TrimSpace(environment) == "" {
			return false, nil
		}
		if s.database == nil {
			return guard.IsBuiltInProductionEnvironment(environment), nil
		}
		return guard.IsProductionEnvironment(ctx, s.database, environment)
	}
}

func canonicalInventoryCommand(command []string) []byte {
	encoded, err := json.Marshal(command)
	if err != nil {
		return []byte("null")
	}
	return encoded
}

func (s *inventoryService) auditTargetExec(ctx context.Context, input inventoryTargetExecRequest, target fleetinventory.Target, sessionID string, execErr error) {
	payload := map[string]any{
		"kind": input.Kind, "action": input.Action, "container": input.Container, "command": input.Command,
	}
	if sessionID != "" {
		payload["sessionId"] = sessionID
	}
	switch {
	case target.Docker != nil:
		payload["target"] = "context/" + target.Docker.Name
	case target.Kubernetes != nil:
		payload["target"] = target.Kubernetes.Namespace + "/" + target.Kubernetes.Pod
	}
	var exitCode *int32
	if execErr != nil {
		payload["error"] = execErr.Error()
		var exitErr *fleetinventory.CommandExitError
		var sessionExitErr *inventoryExecExitError
		switch {
		case errors.As(execErr, &exitErr):
			code := int32(exitErr.Code)
			exitCode = &code
		case errors.As(execErr, &sessionExitErr):
			code := int32(sessionExitErr.code)
			exitCode = &code
		}
	} else {
		zero := int32(0)
		exitCode = &zero
	}
	auditCtx, cancel := context.WithTimeout(ipc.WithUserID(context.WithoutCancel(ctx), ipc.OwnerID(ctx)), 5*time.Second)
	defer cancel()
	_ = s.database.AuditInsert(auditCtx, store.AuditInput{Source: "user", Kind: "inventory_target_exec", Payload: payload, ExitCode: exitCode})
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
