package sync

import (
	"context"
	"sort"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

const (
	CommandCollectAssets       = "sync_collect_assets"
	CommandCollectTombstones   = "sync_collect_tombstones"
	CommandCollectCredentials  = "sync_collect_credentials"
	CommandCollectKnownHosts   = "sync_collect_known_hosts"
	CommandCollectAIProfiles   = "sync_collect_ai_profiles"
	CommandCollectAIPermission = "sync_collect_ai_permission"
	CommandCollectPreferences  = "sync_collect_preferences"
)

const (
	defaultCollectLimit = 256
	maxCollectLimit     = 512
)

// Credential secret read results: withheld when the browser did not request reveal; locked/unavailable must be reported explicitly, never silently dropping credentials.
const (
	CollectSecretWithheld    = "withheld"
	CollectSecretRevealed    = "revealed"
	CollectSecretLocked      = "locked"
	CollectSecretUnavailable = "unavailable"
	CollectSecretError       = "error"
)

// CollectAsset fields align by name with the assetObject payload, so the browser can assemble M117 payloads directly.
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

// CollectTombstone entries align semantically with tombstoneObject: id is the object id, deletedAt is the LWW revision;
// known_host conflict tombstones additionally carry the original loser triple, user-initiated deletion tombstones do not.
type CollectTombstone struct {
	ID         string `json:"id"`
	TargetKind string `json:"targetKind"`
	DeletedAt  int64  `json:"deletedAt"`
	Host       string `json:"host,omitempty"`
	Port       int32  `json:"port,omitempty"`
	KeyType    string `json:"keyType,omitempty"`
}

// CollectCredential carries metadata plus an explicitly stated secret; without a vault on the server the secret is unavailable but the entry is kept.
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

// collectPage cuts one page from the full list sorted by ascending ID: afterId is the last entry's ID of the previous page, stable with no loss or duplication.
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

// CollectAssets lists assets per M117 collect semantics: excludes built-ins and the "current device"; includeDeleted explicitly controls soft-deleted rows.
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

// CollectTombstones merges sync_tombstone and credential_tombstone; on same-ID conflicts the credential tombstone wins,
// matching collectLocalObjects' overwrite semantics keyed by ID.
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

// collectSecretState assesses vault readability: no vault or uninitialized = unavailable, locked = locked.
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

// CollectCredentials lists credential metadata; plaintext is attached via the existing vault reveal path only when revealSecrets is set and the device vault is unlocked,
// the server must never decrypt on the device's behalf, and passwords or DEKs must never travel the sync link.
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

// CollectKnownHost fields align by name with the knownHostObject payload; addedAt is the LWW revision, deletions converge via the tombstone list.
type CollectKnownHost struct {
	ID          string `json:"id"`
	Host        string `json:"host"`
	Port        int32  `json:"port"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
	AddedAt     int64  `json:"addedAt"`
}

// CollectAIProfile carries only profile fields and an explicitly stated apiKey; apiKeySet distinguishes "no key" from "key withheld".
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

// CollectAIProfiles lists AI model profiles; apiKey plaintext is attached only when revealSecrets is set and the device vault is unlocked,
// with the same reveal/locked/withheld/error semantics as credential collection; the server must never decrypt on the device's behalf.
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
