package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// aiProfilesStoreVersion aligns with internal/ai/profiles.StoreVersion; higher versions are owned by newer apps and sync must not rewrite them.
const aiProfilesStoreVersion = 1

// errAIProfilesCorrupt distinguishes "corrupt profile data" (skip and warn) from database errors (fail the whole round).
var errAIProfilesCorrupt = errors.New("AI 模型档案数据损坏")

// aiProfileRecord is the persisted form in the ai.models setting (no revision); fields exactly match profiles.Profile.
type aiProfileRecord struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	BaseURL         string   `json:"baseUrl"`
	APIKey          string   `json:"apiKey"`
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
}

type aiProfilesState struct {
	Version  int               `json:"version"`
	Profiles []aiProfileRecord `json:"profiles"`
	ActiveID *string           `json:"activeId"`
}

func aiProfileRecordFromPayload(payload aiProfileObject) aiProfileRecord {
	return aiProfileRecord{
		ID: payload.ID, Name: payload.Name, BaseURL: payload.BaseURL, APIKey: payload.APIKey,
		Model: payload.Model, FallbackModel: payload.FallbackModel, Temperature: payload.Temperature,
		ReasoningEffort: payload.ReasoningEffort,
		ContextWindow:   payload.ContextWindow, MaxTokens: payload.MaxTokens, Proxy: payload.Proxy, Stream: payload.Stream,
		RequestTimeoutSeconds: payload.RequestTimeoutSeconds, IdleTimeoutSeconds: payload.IdleTimeoutSeconds,
		CircuitFailureThreshold: payload.CircuitFailureThreshold, CircuitCooldownSeconds: payload.CircuitCooldownSeconds,
	}
}

func aiProfilePayloadFromRecord(record aiProfileRecord, updatedAt int64) aiProfileObject {
	return aiProfileObject{
		ID: record.ID, Name: record.Name, BaseURL: record.BaseURL, APIKey: record.APIKey,
		Model: record.Model, FallbackModel: record.FallbackModel, Temperature: record.Temperature,
		ReasoningEffort: record.ReasoningEffort,
		ContextWindow:   record.ContextWindow, MaxTokens: record.MaxTokens, Proxy: record.Proxy, Stream: record.Stream,
		RequestTimeoutSeconds: record.RequestTimeoutSeconds, IdleTimeoutSeconds: record.IdleTimeoutSeconds,
		CircuitFailureThreshold: record.CircuitFailureThreshold, CircuitCooldownSeconds: record.CircuitCooldownSeconds,
		UpdatedAt: updatedAt,
	}
}

func (s aiProfilesState) find(id string) (aiProfileRecord, bool) {
	for _, record := range s.Profiles {
		if record.ID == id {
			return record, true
		}
	}
	return aiProfileRecord{}, false
}

// ensureActive mirrors profiles.Manager: fall back to the first profile when the active one is missing.
func (s *aiProfilesState) ensureActive() {
	if s.ActiveID != nil {
		if _, exists := s.find(*s.ActiveID); exists {
			return
		}
	}
	if len(s.Profiles) == 0 {
		s.ActiveID = nil
		return
	}
	first := s.Profiles[0].ID
	s.ActiveID = &first
}

func (s *aiProfilesState) upsert(record aiProfileRecord) {
	for index := range s.Profiles {
		if s.Profiles[index].ID == record.ID {
			s.Profiles[index] = record
			return
		}
	}
	s.Profiles = append(s.Profiles, record)
}

func (s *aiProfilesState) remove(id string) bool {
	for index := range s.Profiles {
		if s.Profiles[index].ID == id {
			s.Profiles = append(s.Profiles[:index], s.Profiles[index+1:]...)
			return true
		}
	}
	return false
}

// aiProfilesLoad reads the ai.models setting; the revision is the setting row's updated_at (all profiles share one LWW clock).
// found=false means the setting does not exist; callers treat it as empty profiles with revision 0.
func (e *Engine) aiProfilesLoad(ctx context.Context) (aiProfilesState, int64, bool, error) {
	if _, err := e.aiOwnerID(ctx); err != nil {
		return aiProfilesState{}, 0, false, err
	}
	setting := query.Use(e.store.GORM()).Setting
	row, err := setting.WithContext(ctx).Where(setting.Key.Eq(store.AIProfilesSettingKeyForUser(ipc.OwnerID(ctx)))).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return aiProfilesState{Version: aiProfilesStoreVersion, Profiles: []aiProfileRecord{}}, 0, false, nil
	}
	if err != nil {
		return aiProfilesState{}, 0, false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	var state aiProfilesState
	if err := json.Unmarshal([]byte(row.Value), &state); err != nil {
		return aiProfilesState{}, 0, true, fmt.Errorf("%w: %v", errAIProfilesCorrupt, err)
	}
	if state.Version <= 0 || state.Version > aiProfilesStoreVersion {
		return aiProfilesState{}, 0, true, fmt.Errorf("%w: 不支持的版本 %d", errAIProfilesCorrupt, state.Version)
	}
	if state.Profiles == nil {
		state.Profiles = []aiProfileRecord{}
	}
	return state, row.UpdatedAt, true, nil
}

// aiProfilesSave writes the ai.models setting back as a whole, preserving the given revision verbatim; clearing the same-ID deletion tombstone happens in the same transaction.
func (e *Engine) aiProfilesSave(ctx context.Context, state aiProfilesState, updatedAt int64, clearTombstoneID string) error {
	if _, err := e.aiOwnerID(ctx); err != nil {
		return err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "无法编码 AI 模型档案", err)
	}
	return e.store.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
		}).Create(&store.Setting{Key: store.AIProfilesSettingKeyForUser(ipc.OwnerID(ctx)), Value: string(encoded), UpdatedAt: updatedAt}).Error; err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		if clearTombstoneID != "" {
			tombstone := query.Use(tx).SyncTombstone
			if _, err := tombstone.WithContext(ctx).
				Where(tombstone.ID.Eq(clearTombstoneID)).
				Where(ownerConditions(ctx, tombstone.TableName())...).
				Delete(); err != nil {
				return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
			}
		}
		return nil
	})
}

// revealAIProfileKey restores the persisted form to payload plaintext: an empty string passes through, everything else is decrypted via the vault;
// non-envelope or undecryptable values uniformly report a decryption failure.
func (e *Engine) revealAIProfileKey(ctx context.Context, stored string) (string, error) {
	if _, err := e.aiOwnerID(ctx); err != nil {
		return "", err
	}
	if stored == "" {
		return "", nil
	}
	if e.vault == nil {
		return "", ipc.NewError(ipc.CodeVaultLocked, "凭据库不可用，无法读取 AI 模型档案密钥")
	}
	return e.vault.DecryptSecret(ctx, stored)
}

// protectAIProfileKey turns payload plaintext into the persisted envelope; the caller must already have passed requireVault.
func (e *Engine) protectAIProfileKey(ctx context.Context, plaintext string) (string, error) {
	if _, err := e.aiOwnerID(ctx); err != nil {
		return "", err
	}
	if plaintext == "" {
		return "", nil
	}
	return e.vault.EncryptSecret(ctx, plaintext)
}

func (e *Engine) knownHostByID(ctx context.Context, id string) (store.KnownHostRow, bool, error) {
	knownHost := query.Use(e.store.GORM()).KnownHostRow
	row, err := knownHost.WithContext(ctx).
		Where(knownHost.ID.Eq(id)).
		Where(ownerConditions(ctx, knownHost.TableName())...).
		First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.KnownHostRow{}, false, nil
	}
	if err != nil {
		return store.KnownHostRow{}, false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return *row, true, nil
}

// knownHostTombstoneMetaPrefix is the setting key prefix for known_host conflict-tombstone triple metadata.
// The sync_tombstone table only has (id, kind, deleted_at), so the triple must be stored on the side; otherwise a re-propagated tombstone degrades to
// user-deletion semantics and downstream devices would mistakenly delete the newer winning incarnation. Metadata follows the highest revision (guaranteed by the write gate).
const knownHostTombstoneMetaPrefix = "sync.kh_tombstone."

type knownHostTombstoneMeta struct {
	Host    string `json:"host"`
	Port    int32  `json:"port"`
	KeyType string `json:"keyType"`
}

func knownHostTombstoneMetaKey(id string) string {
	return knownHostTombstoneMetaPrefix + id
}

func knownHostTombstoneMetaPrefixForUser(ctx context.Context) string {
	if ownerID := ipc.OwnerID(ctx); ownerID != "" {
		return knownHostTombstoneMetaPrefix + ownerID + "."
	}
	return knownHostTombstoneMetaPrefix
}

func knownHostTombstoneMetaKeyForUser(ctx context.Context, id string) string {
	return knownHostTombstoneMetaPrefixForUser(ctx) + id
}

// knownHostTombstoneRecordTx writes/merges a known_host tombstone in a transaction: sync_tombstone merges revisions via
// max; conflict triple metadata is written only when this revision is not behind the existing tombstone (user deletion
// passes nil, meaning clear metadata). The gate is evaluated after the tombstone merge, so it equals "new revision >= old revision";
// when conflict tombstones of two different triples land concurrently, the metadata deterministically follows the highest revision, never mixing tombstones.
func (e *Engine) knownHostTombstoneRecordTx(ctx context.Context, tx *gorm.DB, id string, deletedAt int64, conflict *knownHostTombstoneMeta) error {
	if err := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "owner_id"}, {Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"deleted_at": store.MaxExpression(tx, "sync_tombstone", "deleted_at", nil),
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "owner_id"}, Value: ipc.OwnerID(ctx)}}},
	}).Create(&store.SyncTombstone{ID: id, Kind: KindKnownHost, DeletedAt: deletedAt, OwnerID: ipc.OwnerID(ctx)}).Error; err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	tombstone := query.Use(tx).SyncTombstone
	merged, err := tombstone.WithContext(ctx).
		Where(tombstone.ID.Eq(id)).
		Where(ownerConditions(ctx, tombstone.TableName())...).
		First()
	if err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if deletedAt < merged.DeletedAt {
		return nil
	}
	if conflict != nil {
		encoded, err := json.Marshal(conflict)
		if err != nil {
			return ipc.WrapError(ipc.CodeInternal, "无法编码冲突项删除标记元数据", err)
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
		}).Create(&store.Setting{Key: knownHostTombstoneMetaKeyForUser(ctx, id), Value: string(encoded), UpdatedAt: deletedAt}).Error; err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		return nil
	}
	setting := query.Use(tx).Setting
	if _, err := setting.WithContext(ctx).Where(setting.Key.Eq(knownHostTombstoneMetaKeyForUser(ctx, id))).Delete(); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

// knownHostTombstoneMetas reads the current user's known_host conflict-tombstone metadata for restoring the triple when tombstones are re-propagated.
func (e *Engine) knownHostTombstoneMetas(ctx context.Context) (map[string]knownHostTombstoneMeta, error) {
	prefix := knownHostTombstoneMetaPrefixForUser(ctx)
	values, err := e.store.SettingListPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	metas := make(map[string]knownHostTombstoneMeta, len(values))
	for key, value := range values {
		var meta knownHostTombstoneMeta
		if err := json.Unmarshal([]byte(value), &meta); err != nil {
			continue
		}
		metas[strings.TrimPrefix(key, prefix)] = meta
	}
	return metas, nil
}

// knownHostTombstoneLoser tombstones the loser of a triple conflict at the winner's revision (an existing later tombstone is kept).
// A local live row with the same ID (an incarnation with a different triple than the loser) must not coexist with a same-ID tombstone: the ambiguous
// object slot would keep the server from converging, and a later same-ID tombstone would mistakenly delete it. The incarnation yields its original ID
// in the same transaction - the trusted content is preserved verbatim under a fresh ID (same revision, no duplicate object), and the original ID is
// tombstoned for the remote loser; when other devices pull that tombstone, the original-ID row is already gone locally, so they only merge the tombstone
// record and never touch the re-IDed incarnation.
func (e *Engine) knownHostTombstoneLoser(ctx context.Context, payload knownHostObject, incarnation *store.KnownHostRow, winnerAddedAt int64) error {
	return e.store.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		knownHost := query.Use(tx).KnownHostRow
		if incarnation != nil {
			newID, err := e.knownHostReincarnationID(incarnation)
			if err != nil {
				return err
			}
			if _, err := knownHost.WithContext(ctx).
				Where(knownHost.ID.Eq(incarnation.ID)).
				Where(ownerConditions(ctx, knownHost.TableName())...).
				UpdateColumns(map[string]any{"id": newID}); err != nil {
				return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
			}
		} else if _, err := knownHost.WithContext(ctx).
			Where(knownHost.ID.Eq(payload.ID), knownHost.Host.Eq(payload.Host), knownHost.Port.Eq(payload.Port), knownHost.KeyType.Eq(payload.KeyType)).
			Where(ownerConditions(ctx, knownHost.TableName())...).
			Delete(); err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		return e.knownHostTombstoneRecordTx(ctx, tx, payload.ID, winnerAddedAt, &knownHostTombstoneMeta{Host: payload.Host, Port: payload.Port, KeyType: payload.KeyType})
	})
}

// knownHostReincarnationID generates a new ID for the incarnation: content and addedAt stay unchanged, but the new payload must beat the old payload
// in the same-revision hash tiebreak. Old-ID replicas on other devices therefore only take the displaced path (keeping the new ID) when meeting the
// re-IDed object and never tombstone the new ID in reverse; the path destroying both old and new IDs is constructively excluded.
func (e *Engine) knownHostReincarnationID(incarnation *store.KnownHostRow) (string, error) {
	oldPayload, err := marshalObject(knownHostObject{
		ID: incarnation.ID, Host: incarnation.Host, Port: incarnation.Port, KeyType: incarnation.KeyType,
		Fingerprint: incarnation.Fingerprint, AddedAt: incarnation.AddedAt,
	})
	if err != nil {
		return "", err
	}
	for {
		candidate := ids.New()
		newPayload, err := marshalObject(knownHostObject{
			ID: candidate, Host: incarnation.Host, Port: incarnation.Port, KeyType: incarnation.KeyType,
			Fingerprint: incarnation.Fingerprint, AddedAt: incarnation.AddedAt,
		})
		if err != nil {
			return "", err
		}
		if remoteWins(incarnation.AddedAt, incarnation.AddedAt, newPayload, oldPayload) {
			return candidate, nil
		}
	}
}

// knownHostUpsert writes idempotently by ID and preserves the peer revision added_at; the loser row of a triple conflict and its tombstone are cleared in the same transaction.
func (e *Engine) knownHostUpsert(ctx context.Context, payload knownHostObject, displacedID string) error {
	return e.store.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		knownHost := query.Use(tx).KnownHostRow
		if displacedID != "" {
			if _, err := knownHost.WithContext(ctx).
				Where(knownHost.ID.Eq(displacedID)).
				Where(ownerConditions(ctx, knownHost.TableName())...).
				Delete(); err != nil {
				return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
			}
			if err := e.knownHostTombstoneRecordTx(ctx, tx, displacedID, payload.AddedAt, &knownHostTombstoneMeta{Host: payload.Host, Port: payload.Port, KeyType: payload.KeyType}); err != nil {
				return err
			}
		}
		row := &store.KnownHostRow{ID: payload.ID, Host: payload.Host, Port: payload.Port, KeyType: payload.KeyType, Fingerprint: payload.Fingerprint, AddedAt: payload.AddedAt, OwnerID: ipc.OwnerID(ctx)}
		if err := store.UpsertOwnerGuarded(ctx, tx, row, []string{"host", "port", "key_type", "fingerprint", "added_at", "op_token"}, "added_at"); err != nil {
			return err
		}
		tombstone := query.Use(tx).SyncTombstone
		if _, err := tombstone.WithContext(ctx).
			Where(tombstone.ID.Eq(payload.ID)).
			Where(ownerConditions(ctx, tombstone.TableName())...).
			Delete(); err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		setting := query.Use(tx).Setting
		if _, err := setting.WithContext(ctx).Where(setting.Key.Eq(knownHostTombstoneMetaKeyForUser(ctx, payload.ID))).Delete(); err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		return nil
	})
}
