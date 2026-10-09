package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// aiProfilesStoreVersion 与 internal/ai/profiles.StoreVersion 对齐; 更高版本由更新的应用所有, 同步不得改写。
const aiProfilesStoreVersion = 1

// errAIProfilesCorrupt 区分「档案数据损坏」(跳过并告警)与数据库错误(整轮失败)。
var errAIProfilesCorrupt = errors.New("AI 模型档案数据损坏")

// aiProfileRecord 是 ai.models 设置中的持久化形态(无修订号), 字段与 profiles.Profile 完全一致。
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

// ensureActive 与 profiles.Manager 同款: 当前激活档案缺失时回退到首个档案。
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

// aiProfilesLoad 读取 ai.models 设置; 修订号即设置行的 updated_at(整档案共享一个 LWW 时钟)。
// found=false 表示设置不存在, 调用方按空档案、修订号 0 处理。
func (e *Engine) aiProfilesLoad(ctx context.Context) (aiProfilesState, int64, bool, error) {
	var value string
	var updatedAt int64
	err := e.store.DB().QueryRowContext(ctx,
		"SELECT value, updated_at FROM setting WHERE key = ?", store.AIProfilesSettingKey).
		Scan(&value, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return aiProfilesState{Version: aiProfilesStoreVersion, Profiles: []aiProfileRecord{}}, 0, false, nil
	}
	if err != nil {
		return aiProfilesState{}, 0, false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	var state aiProfilesState
	if err := json.Unmarshal([]byte(value), &state); err != nil {
		return aiProfilesState{}, 0, true, fmt.Errorf("%w: %v", errAIProfilesCorrupt, err)
	}
	if state.Version < 0 || state.Version > aiProfilesStoreVersion {
		return aiProfilesState{}, 0, true, fmt.Errorf("%w: 不支持的版本 %d", errAIProfilesCorrupt, state.Version)
	}
	if state.Profiles == nil {
		state.Profiles = []aiProfileRecord{}
	}
	return state, updatedAt, true, nil
}

// aiProfilesSave 整体写回 ai.models 设置并原样保留指定修订号; 与清除同 ID 删除墓碑同一事务。
func (e *Engine) aiProfilesSave(ctx context.Context, state aiProfilesState, updatedAt int64, clearTombstoneID string) error {
	if state.Version <= 0 || state.Version > aiProfilesStoreVersion {
		state.Version = aiProfilesStoreVersion
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "无法编码 AI 模型档案", err)
	}
	tx, err := e.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		store.AIProfilesSettingKey, string(encoded), updatedAt); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if clearTombstoneID != "" {
		if _, err := tx.ExecContext(ctx, "DELETE FROM sync_tombstone WHERE id = ?", clearTombstoneID); err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
	}
	return tx.Commit()
}

// revealAIProfileKey 把落盘形态还原为载荷明文: 空串原样, enc:v1: 信封走凭据库;
// 历史明文不再识别, 报错引导重新保存。
func (e *Engine) revealAIProfileKey(ctx context.Context, stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if !strings.HasPrefix(stored, store.SecretEnvelopePrefix) {
		return "", ipc.NewError(ipc.CodeCrypto, "该档案的 API Key 为旧版明文存储，请在设置中重新保存")
	}
	if e.vault == nil {
		return "", ipc.NewError(ipc.CodeVaultLocked, "凭据库不可用, 无法读取 AI 模型档案密钥")
	}
	return e.vault.DecryptSecret(ctx, stored)
}

// protectAIProfileKey 把载荷明文转为落盘信封; 调用方必须已通过 requireVault 把关。
func (e *Engine) protectAIProfileKey(ctx context.Context, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	return e.vault.EncryptSecret(ctx, plaintext)
}

func (e *Engine) knownHostByID(ctx context.Context, id string) (store.KnownHostRow, bool, error) {
	var row store.KnownHostRow
	err := e.store.DB().QueryRowContext(ctx,
		"SELECT id, host, port, key_type, fingerprint, added_at FROM known_host WHERE id = ?", id).
		Scan(&row.ID, &row.Host, &row.Port, &row.KeyType, &row.Fingerprint, &row.AddedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.KnownHostRow{}, false, nil
	}
	if err != nil {
		return store.KnownHostRow{}, false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return row, true, nil
}

// knownHostTombstoneMetaPrefix 是 known_host 冲突墓碑三元组元数据的 setting 键前缀。
// sync_tombstone 表只有 (id, kind, deleted_at), 三元组必须侧存, 否则再传播的墓碑退化为
// 用户删除语义, 下游设备会把胜出的较新化身误删。元数据跟随修订号最大者(由写入门保证)。
const knownHostTombstoneMetaPrefix = "sync.kh_tombstone."

type knownHostTombstoneMeta struct {
	Host    string `json:"host"`
	Port    int32  `json:"port"`
	KeyType string `json:"keyType"`
}

func knownHostTombstoneMetaKey(id string) string {
	return knownHostTombstoneMetaPrefix + id
}

// knownHostTombstoneRecordTx 在事务内写入/合并 known_host 墓碑: sync_tombstone 按
// scalarMax 合并修订号; 冲突三元组元数据仅在本次修订号不落后于现有墓碑时写入(用户删除
// 传 nil, 表示清除元数据)。门条件在墓碑合并后求值, 因此等价于「新修订号 >= 旧修订号」,
// 两个不同三元组的冲突墓碑并发落地时, 元数据确定性地跟随修订号最大者, 不拼出混合墓碑。
func (e *Engine) knownHostTombstoneRecordTx(ctx context.Context, tx *sql.Tx, id string, deletedAt int64, conflict *knownHostTombstoneMeta) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO sync_tombstone(id, kind, deleted_at) VALUES(?,?,?)
ON CONFLICT(id) DO UPDATE SET deleted_at = `+scalarMax(e.store.Backend())+`(sync_tombstone.deleted_at, excluded.deleted_at)`,
		id, KindKnownHost, deletedAt); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if conflict != nil {
		encoded, err := json.Marshal(conflict)
		if err != nil {
			return ipc.WrapError(ipc.CodeInternal, "无法编码冲突墓碑元数据", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at
WHERE ? >= (SELECT deleted_at FROM sync_tombstone WHERE id = ?)`,
			knownHostTombstoneMetaKey(id), string(encoded), deletedAt, deletedAt, id); err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM setting WHERE key = ? AND ? >= (SELECT deleted_at FROM sync_tombstone WHERE id = ?)`,
		knownHostTombstoneMetaKey(id), deletedAt, id); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

// knownHostTombstoneMetas 读取全部 known_host 冲突墓碑元数据, 供墓碑再传播时还原三元组。
func (e *Engine) knownHostTombstoneMetas(ctx context.Context) (map[string]knownHostTombstoneMeta, error) {
	values, err := e.store.SettingListPrefix(ctx, knownHostTombstoneMetaPrefix)
	if err != nil {
		return nil, err
	}
	metas := make(map[string]knownHostTombstoneMeta, len(values))
	for key, value := range values {
		var meta knownHostTombstoneMeta
		if err := json.Unmarshal([]byte(value), &meta); err != nil {
			continue
		}
		metas[strings.TrimPrefix(key, knownHostTombstoneMetaPrefix)] = meta
	}
	return metas, nil
}

// knownHostTombstoneLoser 为三元组冲突的败者按胜者修订号立碑(已存在的更晚墓碑保留)。
// 同 ID 的本地存活行(与败者不同三元组的化身)不得与同 ID 墓碑共存: 对象槽位二义会使
// 服务端不收敛, 后续同 ID 墓碑也会误删它。化身在同一事务内让出原 ID——信任内容以全新 ID
// 原样保留(修订号不变, 不产生重复对象), 原 ID 立碑给远端败者; 其他设备拉取该墓碑时
// 本地已无原 ID 行, 只合并墓碑记录, 不会触碰 re-ID 后的化身。
func (e *Engine) knownHostTombstoneLoser(ctx context.Context, payload knownHostObject, incarnation *store.KnownHostRow, winnerAddedAt int64) error {
	tx, err := e.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	defer func() { _ = tx.Rollback() }()
	if incarnation != nil {
		newID, err := e.knownHostReincarnationID(incarnation)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE known_host SET id = ? WHERE id = ?", newID, incarnation.ID); err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
	} else if _, err := tx.ExecContext(ctx, "DELETE FROM known_host WHERE id = ? AND host = ? AND port = ? AND key_type = ?",
		payload.ID, payload.Host, payload.Port, payload.KeyType); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	meta := &knownHostTombstoneMeta{Host: payload.Host, Port: payload.Port, KeyType: payload.KeyType}
	if err := e.knownHostTombstoneRecordTx(ctx, tx, payload.ID, winnerAddedAt, meta); err != nil {
		return err
	}
	return tx.Commit()
}

// knownHostReincarnationID 为化身生成新 ID: 内容与 addedAt 不变, 但新载荷必须在同修订号
// 哈希决胜中必胜旧载荷。其他设备上的旧 ID 副本遇到 re-ID 对象时因此只能走 displaced
// 路径(保留新 ID), 不会反向把新 ID 墓碑化; 新旧 ID 同毁的路径被构造性排除。
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

// knownHostUpsert 按 ID 幂等写入并保留对端修订号 added_at; 三元组冲突的败者行与其墓碑在同一事务清除。
func (e *Engine) knownHostUpsert(ctx context.Context, payload knownHostObject, displacedID string) error {
	tx, err := e.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	defer func() { _ = tx.Rollback() }()
	if displacedID != "" {
		if _, err := tx.ExecContext(ctx, "DELETE FROM known_host WHERE id = ?", displacedID); err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		meta := &knownHostTombstoneMeta{Host: payload.Host, Port: payload.Port, KeyType: payload.KeyType}
		if err := e.knownHostTombstoneRecordTx(ctx, tx, displacedID, payload.AddedAt, meta); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO known_host(id, host, port, key_type, fingerprint, added_at) VALUES(?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET host=excluded.host, port=excluded.port, key_type=excluded.key_type,
fingerprint=excluded.fingerprint, added_at=excluded.added_at`,
		payload.ID, payload.Host, payload.Port, payload.KeyType, payload.Fingerprint, payload.AddedAt); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sync_tombstone WHERE id = ?", payload.ID); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM setting WHERE key = ?", knownHostTombstoneMetaKey(payload.ID)); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return tx.Commit()
}
