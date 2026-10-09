package sync

import (
	"context"
	"sort"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

const (
	CommandCollectAssets      = "sync_collect_assets"
	CommandCollectTombstones  = "sync_collect_tombstones"
	CommandCollectCredentials = "sync_collect_credentials"
	CommandCollectKnownHosts  = "sync_collect_known_hosts"
	CommandCollectAIProfiles  = "sync_collect_ai_profiles"
)

const (
	defaultCollectLimit = 256
	maxCollectLimit     = 512
)

// 凭据秘密读取结果: 浏览器未请求 reveal 时为 withheld; 锁定/不可用必须明确给出, 不得静默丢凭据。
const (
	CollectSecretWithheld    = "withheld"
	CollectSecretRevealed    = "revealed"
	CollectSecretLocked      = "locked"
	CollectSecretUnavailable = "unavailable"
	CollectSecretError       = "error"
)

// CollectAsset 字段与 assetObject 载荷同名对齐, 浏览器可直接组装 M117 载荷。
type CollectAsset struct {
	ID          string  `json:"id"`
	GroupID     *string `json:"groupId,omitempty"`
	Kind        string  `json:"kind"`
	Name        string  `json:"name"`
	Host        *string `json:"host,omitempty"`
	Port        *int32  `json:"port,omitempty"`
	Username    *string `json:"username,omitempty"`
	AuthKind    *string `json:"authKind,omitempty"`
	KeyPath     *string `json:"keyPath,omitempty"`
	CredID      *string `json:"credId,omitempty"`
	OptionsJSON string  `json:"optionsJson"`
	Tags        string  `json:"tags"`
	Note        string  `json:"note"`
	Sort        int64   `json:"sort"`
	CreatedAt   int64   `json:"createdAt"`
	UpdatedAt   int64   `json:"updatedAt"`
	DeletedAt   *int64  `json:"deletedAt,omitempty"`
}

// CollectTombstone 条目与 tombstoneObject 语义对齐: id 即对象 id, deletedAt 即 LWW 修订号;
// known_host 冲突墓碑额外携带原败者三元组, 用户主动删除墓碑则无。
type CollectTombstone struct {
	ID         string `json:"id"`
	TargetKind string `json:"targetKind"`
	DeletedAt  int64  `json:"deletedAt"`
	Host       string `json:"host,omitempty"`
	Port       int32  `json:"port,omitempty"`
	KeyType    string `json:"keyType,omitempty"`
}

// CollectCredential 只含元数据与明确状态的 secret; 服务端无 vault 时秘密不可得但条目不丢。
type CollectCredential struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	UpdatedAt   int64   `json:"updatedAt"`
	Secret      *string `json:"secret,omitempty"`
	SecretState string  `json:"secretState"`
}

type CollectAssetsRequest struct {
	IncludeDeleted bool   `json:"includeDeleted"`
	AfterID        string `json:"afterId,omitempty"`
	Limit          int    `json:"limit,omitempty"`
}

type CollectTombstonesRequest struct {
	AfterID string `json:"afterId,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type CollectCredentialsRequest struct {
	RevealSecrets bool   `json:"revealSecrets"`
	AfterID       string `json:"afterId,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

type CollectAssetsResult struct {
	Assets      []CollectAsset `json:"assets"`
	HasMore     bool           `json:"hasMore"`
	NextAfterID string         `json:"nextAfterId,omitempty"`
}

type CollectTombstonesResult struct {
	Tombstones  []CollectTombstone `json:"tombstones"`
	HasMore     bool               `json:"hasMore"`
	NextAfterID string             `json:"nextAfterId,omitempty"`
}

type CollectCredentialsResult struct {
	Credentials []CollectCredential `json:"credentials"`
	HasMore     bool                `json:"hasMore"`
	NextAfterID string              `json:"nextAfterId,omitempty"`
}

func collectLimit(requested int) int {
	if requested <= 0 {
		return defaultCollectLimit
	}
	if requested > maxCollectLimit {
		return maxCollectLimit
	}
	return requested
}

// collectPage 在按 ID 升序的完整清单上切一页: afterId 为上一页最后一条的 ID, 稳定不丢不重。
func collectPage[T any](entries []T, idOf func(T) string, afterID string, limit int) (page []T, hasMore bool, nextAfterID string) {
	start := 0
	if afterID != "" {
		start = sort.Search(len(entries), func(index int) bool { return idOf(entries[index]) > afterID })
	}
	end := start + limit
	if end < len(entries) {
		hasMore = true
	} else {
		end = len(entries)
	}
	page = entries[start:end]
	if len(page) > 0 {
		nextAfterID = idOf(page[len(page)-1])
	}
	return page, hasMore, nextAfterID
}

func collectAssetFromRow(row store.AssetRow) CollectAsset {
	return CollectAsset{
		ID: row.ID, GroupID: row.GroupID, Kind: row.Kind, Name: row.Name,
		Host: row.Host, Port: row.Port, Username: row.Username, AuthKind: row.AuthKind,
		KeyPath: row.KeyPath, CredID: row.CredID, OptionsJSON: row.OptionsJSON,
		Tags: row.Tags, Note: row.Note, Sort: row.Sort, CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt,
	}
}

// CollectAssets 按 M117 collect 语义列出资产: 排除内置与「当前设备」, includeDeleted 显式控制软删。
func (s *Service) CollectAssets(ctx context.Context, request CollectAssetsRequest) (CollectAssetsResult, error) {
	rows, err := s.store.AssetList(ctx, request.IncludeDeleted)
	if err != nil {
		return CollectAssetsResult{}, err
	}
	entries := make([]CollectAsset, 0, len(rows))
	for _, row := range rows {
		if row.Builtin || row.ID == store.BuiltinLocalAssetID {
			continue
		}
		entries = append(entries, collectAssetFromRow(row))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	page, hasMore, nextAfterID := collectPage(entries, func(entry CollectAsset) string { return entry.ID }, request.AfterID, collectLimit(request.Limit))
	return CollectAssetsResult{Assets: page, HasMore: hasMore, NextAfterID: nextAfterID}, nil
}

// CollectTombstones 合并 sync_tombstone 与 credential_tombstone; 同 ID 冲突时凭据墓碑胜出,
// 与 collectLocalObjects 以 ID 为键的覆盖语义一致。
func (s *Service) CollectTombstones(ctx context.Context, request CollectTombstonesRequest) (CollectTombstonesResult, error) {
	merged := map[string]CollectTombstone{}
	knownHostMetas, err := s.engine.knownHostTombstoneMetas(ctx)
	if err != nil {
		return CollectTombstonesResult{}, err
	}
	syncRows, err := s.engine.syncTombstoneList(ctx)
	if err != nil {
		return CollectTombstonesResult{}, err
	}
	for _, row := range syncRows {
		entry := CollectTombstone{ID: row.ID, TargetKind: row.Kind, DeletedAt: row.DeletedAt}
		if row.Kind == KindKnownHost {
			if meta, found := knownHostMetas[row.ID]; found {
				entry.Host, entry.Port, entry.KeyType = meta.Host, meta.Port, meta.KeyType
			}
		}
		merged[row.ID] = entry
	}
	credentialRows, err := s.store.CredentialTombstoneList(ctx)
	if err != nil {
		return CollectTombstonesResult{}, err
	}
	for _, row := range credentialRows {
		merged[row.ID] = CollectTombstone{ID: row.ID, TargetKind: KindCredential, DeletedAt: row.DeletedAt}
	}
	entries := make([]CollectTombstone, 0, len(merged))
	for _, entry := range merged {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	page, hasMore, nextAfterID := collectPage(entries, func(entry CollectTombstone) string { return entry.ID }, request.AfterID, collectLimit(request.Limit))
	return CollectTombstonesResult{Tombstones: page, HasMore: hasMore, NextAfterID: nextAfterID}, nil
}

// collectSecretState 评估 vault 可读性: 无 vault 或未初始化 = unavailable, 锁定 = locked。
func (s *Service) collectSecretState() string {
	if s.vault == nil {
		return CollectSecretUnavailable
	}
	status := s.vault.Status()
	if !status.Initialized {
		return CollectSecretUnavailable
	}
	if !status.Unlocked {
		return CollectSecretLocked
	}
	return CollectSecretRevealed
}

// CollectCredentials 列出凭据元数据; 只有 revealSecrets 且设备 vault 已解锁时才经既有
// vault reveal 路径附带明文, 服务端不得代理解密, 也不得经同步链路发送口令或 DEK。
func (s *Service) CollectCredentials(ctx context.Context, request CollectCredentialsRequest) (CollectCredentialsResult, error) {
	rows, err := s.store.CredentialList(ctx)
	if err != nil {
		return CollectCredentialsResult{}, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	state := CollectSecretWithheld
	if request.RevealSecrets {
		state = s.collectSecretState()
	}
	entries := make([]CollectCredential, 0, len(rows))
	for _, row := range rows {
		entry := CollectCredential{ID: row.ID, Name: row.Name, Kind: row.Kind, UpdatedAt: row.UpdatedAt, SecretState: state}
		if state == CollectSecretRevealed {
			secret, err := s.vault.DecryptCredentialString(ctx, row)
			if err != nil {
				entry.SecretState = CollectSecretError
			} else {
				entry.Secret = &secret
			}
		}
		entries = append(entries, entry)
	}
	page, hasMore, nextAfterID := collectPage(entries, func(entry CollectCredential) string { return entry.ID }, request.AfterID, collectLimit(request.Limit))
	return CollectCredentialsResult{Credentials: page, HasMore: hasMore, NextAfterID: nextAfterID}, nil
}

// CollectKnownHost 字段与 knownHostObject 载荷同名对齐; addedAt 即 LWW 修订号, 删除经 tombstone 清单收敛。
type CollectKnownHost struct {
	ID          string `json:"id"`
	Host        string `json:"host"`
	Port        int32  `json:"port"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
	AddedAt     int64  `json:"addedAt"`
}

// CollectAIProfile 只含档案字段与明确状态的 apiKey; apiKeySet 区分「无密钥」与「密钥被扣留」。
type CollectAIProfile struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	BaseURL         string   `json:"baseUrl"`
	Model           string   `json:"model"`
	FallbackModel   string   `json:"fallbackModel,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	ReasoningEffort string   `json:"reasoningEffort,omitempty"`
	ContextWindow   uint64   `json:"contextWindow"`
	MaxTokens       *int     `json:"maxTokens,omitempty"`
	Proxy           *string  `json:"proxy"`
	Stream          bool     `json:"stream"`

	RequestTimeoutSeconds *int `json:"requestTimeoutSeconds,omitempty"`
	IdleTimeoutSeconds    *int `json:"idleTimeoutSeconds,omitempty"`

	CircuitFailureThreshold *int `json:"circuitFailureThreshold,omitempty"`
	CircuitCooldownSeconds  *int `json:"circuitCooldownSeconds,omitempty"`

	UpdatedAt   int64   `json:"updatedAt"`
	APIKey      *string `json:"apiKey,omitempty"`
	APIKeySet   bool    `json:"apiKeySet"`
	APIKeyState string  `json:"apiKeyState"`
}

type CollectKnownHostsRequest struct {
	AfterID string `json:"afterId,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type CollectAIProfilesRequest struct {
	RevealSecrets bool   `json:"revealSecrets"`
	AfterID       string `json:"afterId,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

type CollectKnownHostsResult struct {
	KnownHosts  []CollectKnownHost `json:"knownHosts"`
	HasMore     bool               `json:"hasMore"`
	NextAfterID string             `json:"nextAfterId,omitempty"`
}

type CollectAIProfilesResult struct {
	Profiles    []CollectAIProfile `json:"profiles"`
	HasMore     bool               `json:"hasMore"`
	NextAfterID string             `json:"nextAfterId,omitempty"`
}

func (s *Service) CollectKnownHosts(ctx context.Context, request CollectKnownHostsRequest) (CollectKnownHostsResult, error) {
	rows, err := s.store.KnownHostList(ctx)
	if err != nil {
		return CollectKnownHostsResult{}, err
	}
	entries := make([]CollectKnownHost, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, CollectKnownHost{
			ID: row.ID, Host: row.Host, Port: row.Port, KeyType: row.KeyType,
			Fingerprint: row.Fingerprint, AddedAt: row.AddedAt,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	page, hasMore, nextAfterID := collectPage(entries, func(entry CollectKnownHost) string { return entry.ID }, request.AfterID, collectLimit(request.Limit))
	return CollectKnownHostsResult{KnownHosts: page, HasMore: hasMore, NextAfterID: nextAfterID}, nil
}

// CollectAIProfiles 列出 AI 模型档案; 只有 revealSecrets 且设备凭据库已解锁时才附带 apiKey 明文,
// 与凭据收集同一套 reveal/locked/withheld/error 语义, 服务端不得代理解密。
func (s *Service) CollectAIProfiles(ctx context.Context, request CollectAIProfilesRequest) (CollectAIProfilesResult, error) {
	state, revision, _, err := s.engine.aiProfilesLoad(ctx)
	if err != nil {
		return CollectAIProfilesResult{}, err
	}
	secretState := CollectSecretWithheld
	if request.RevealSecrets {
		secretState = s.collectSecretState()
	}
	entries := make([]CollectAIProfile, 0, len(state.Profiles))
	for _, record := range state.Profiles {
		entry := CollectAIProfile{
			ID: record.ID, Name: record.Name, BaseURL: record.BaseURL, Model: record.Model,
			FallbackModel: record.FallbackModel, Temperature: record.Temperature,
			ReasoningEffort: record.ReasoningEffort,
			ContextWindow:   record.ContextWindow, MaxTokens: record.MaxTokens, Proxy: record.Proxy,
			Stream: record.Stream, RequestTimeoutSeconds: record.RequestTimeoutSeconds,
			IdleTimeoutSeconds:      record.IdleTimeoutSeconds,
			CircuitFailureThreshold: record.CircuitFailureThreshold, CircuitCooldownSeconds: record.CircuitCooldownSeconds,
			UpdatedAt: revision, APIKeySet: record.APIKey != "", APIKeyState: secretState,
		}
		if secretState == CollectSecretRevealed && record.APIKey != "" {
			key, err := s.engine.revealAIProfileKey(ctx, record.APIKey)
			if err != nil {
				entry.APIKeyState = CollectSecretError
			} else {
				entry.APIKey = &key
			}
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	page, hasMore, nextAfterID := collectPage(entries, func(entry CollectAIProfile) string { return entry.ID }, request.AfterID, collectLimit(request.Limit))
	return CollectAIProfilesResult{Profiles: page, HasMore: hasMore, NextAfterID: nextAfterID}, nil
}
