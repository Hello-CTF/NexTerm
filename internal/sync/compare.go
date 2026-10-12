package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const CommandCompare = "sync_compare"

// 对比行状态与浏览器 CompareConsole 的 RowState 一致;
// remote-undecryptable 为桌面端新增: 云端对象用当前 DEK 解不开时如实成行, 不算错误。
const (
	CompareStateLocalOnly           = "local-only"
	CompareStateRemoteOnly          = "remote-only"
	CompareStateSame                = "same"
	CompareStateLocalNewer          = "local-newer"
	CompareStateRemoteNewer         = "remote-newer"
	CompareStateLocalDeleted        = "local-deleted"
	CompareStateRemoteDeleted       = "remote-deleted"
	CompareStateRemoteUndecryptable = "remote-undecryptable"
)

// CompareRow 是一条本机/云端对象的对比结果; 修订号即 LWW 时钟值, 0 表示该侧不存在或不可读。
type CompareRow struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	State         string `json:"state"`
	UpdatedLocal  int64  `json:"updatedAtLocal,omitempty"`
	UpdatedRemote int64  `json:"updatedAtRemote,omitempty"`
}

// CompareResult 是 sync_compare 的返回: rows 按「差异优先, 名称次之」排序(与浏览器一致),
// warnings 携带本地收集与云端拉取告警, remote 是本轮参与对比的云端对象数。
type CompareResult struct {
	Rows     []CompareRow `json:"rows"`
	Warnings []string     `json:"warnings,omitempty"`
	Remote   int          `json:"remote"`
}

// compareEntity 是一侧实体的判定视图: 明文只用于哈希/修订号/名称, 不出本进程。
type compareEntity struct {
	kind        string
	plaintext   []byte
	payloadHash string
	revision    int64
	deleted     bool
	name        string
}

func newCompareEntity(id, kind string, plaintext []byte) compareEntity {
	return compareEntity{
		kind:        kind,
		plaintext:   plaintext,
		payloadHash: objectPayloadHash(plaintext),
		revision:    localRevision(kind, plaintext),
		deleted:     compareDeleted(kind, plaintext),
		name:        compareName(id, kind, plaintext),
	}
}

// compareName 从明文提取行名: 组/资产/片段/凭据/AI 档案用 name, known_host 用 host:port,
// 偏好用 key, AI 权限为全局单例, 会话记录带资产名, 墓碑退化为对象 id。
func compareName(id, kind string, plaintext []byte) string {
	switch kind {
	case KindTombstone:
		return id
	case KindTranscript:
		var payload struct {
			AssetName string `json:"assetName"`
		}
		if err := json.Unmarshal(plaintext, &payload); err == nil && payload.AssetName != "" {
			return payload.AssetName + " 的会话记录"
		}
	case KindKnownHost:
		var payload struct {
			Host string `json:"host"`
			Port int32  `json:"port"`
		}
		if err := json.Unmarshal(plaintext, &payload); err == nil && payload.Host != "" {
			return fmt.Sprintf("%s:%d", payload.Host, payload.Port)
		}
	case KindPreference:
		var payload preferenceObject
		if err := json.Unmarshal(plaintext, &payload); err == nil && payload.Key != "" {
			return payload.Key
		}
	case KindAIPermission:
		return "全局 AI 权限"
	default:
		var payload struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(plaintext, &payload); err == nil && payload.Name != "" {
			return payload.Name
		}
	}
	return id
}

// compareDeleted 判定该侧内容是否为删除形态: 墓碑对象或带 deletedAt 的软删资产。
func compareDeleted(kind string, plaintext []byte) bool {
	if kind == KindTombstone {
		return true
	}
	var payload struct {
		DeletedAt *int64 `json:"deletedAt"`
	}
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return false
	}
	return payload.DeletedAt != nil
}

// compareLocalWins 与浏览器 localWins 同一裁决: 同 hash 无需推送; 会话记录完整内容胜省略版;
// 其余修订号大者胜, 平手按载荷哈希字典序决胜。
func compareLocalWins(local, remote compareEntity) bool {
	if local.payloadHash == remote.payloadHash {
		return false
	}
	if local.kind == KindTranscript {
		localFull, remoteFull := transcriptContentFull(local.plaintext), transcriptContentFull(remote.plaintext)
		if localFull != remoteFull {
			return localFull
		}
	}
	if local.revision != remote.revision {
		return local.revision > remote.revision
	}
	return local.payloadHash > remote.payloadHash
}

func transcriptContentFull(plaintext []byte) bool {
	var payload struct {
		ContentOmitted bool `json:"contentOmitted"`
	}
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return true
	}
	return !payload.ContentOmitted
}

// compareState 与浏览器 buildRows 同态: 同 hash 即一致; 删除形态参与时对侧胜给「已删」,
// 本侧胜给「较新」; 其余按 LWW 胜方给「较新」。
func compareState(local, remote compareEntity) string {
	if local.payloadHash == remote.payloadHash {
		return CompareStateSame
	}
	wins := compareLocalWins(local, remote)
	switch {
	case remote.deleted:
		if wins {
			return CompareStateLocalNewer
		}
		return CompareStateRemoteDeleted
	case local.deleted:
		if wins {
			return CompareStateLocalDeleted
		}
		return CompareStateRemoteNewer
	default:
		if wins {
			return CompareStateLocalNewer
		}
		return CompareStateRemoteNewer
	}
}

// buildCompareRows 合并本机与云端实体为对比行: 差异在前(same 排后), 名称字典次序之。
// undecryptable 是解不开的云端对象 id 集合: 本机同名行用本机名称如实标注, 纯云端行退化为 id。
func buildCompareRows(local map[string]compareEntity, remote map[string]compareEntity, undecryptable map[string]bool) []CompareRow {
	rows := make([]CompareRow, 0, len(local)+len(remote)+len(undecryptable))
	for id, l := range local {
		if r, onRemote := remote[id]; onRemote {
			delete(remote, id)
			rows = append(rows, CompareRow{ID: id, Kind: l.kind, Name: l.name, State: compareState(l, r), UpdatedLocal: l.revision, UpdatedRemote: r.revision})
			continue
		}
		if undecryptable[id] {
			delete(undecryptable, id)
			rows = append(rows, CompareRow{ID: id, Kind: l.kind, Name: l.name, State: CompareStateRemoteUndecryptable, UpdatedLocal: l.revision})
			continue
		}
		state := CompareStateLocalOnly
		if l.deleted {
			state = CompareStateLocalDeleted
		}
		rows = append(rows, CompareRow{ID: id, Kind: l.kind, Name: l.name, State: state, UpdatedLocal: l.revision})
	}
	for id, r := range remote {
		state := CompareStateRemoteOnly
		if r.deleted {
			state = CompareStateRemoteDeleted
		}
		rows = append(rows, CompareRow{ID: id, Kind: r.kind, Name: r.name, State: state, UpdatedRemote: r.revision})
	}
	for id := range undecryptable {
		rows = append(rows, CompareRow{ID: id, Name: id, State: CompareStateRemoteUndecryptable})
	}
	sort.Slice(rows, func(i, j int) bool {
		leftSame, rightSame := rows[i].State == CompareStateSame, rows[j].State == CompareStateSame
		if leftSame != rightSame {
			return !leftSame
		}
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].ID < rows[j].ID
	})
	return rows
}

// Compare 输出本机与云端对象的逐条对比(仅桌面端): 本机侧经与同步轮相同的 collectLocalObjects
// 收集, 云端侧拉取全量对象并用引擎会话 DEK 解密; 判定规则与浏览器 CompareConsole 一致。
// 对比是只读视图: 不推进游标、不写对账状态。会话过期自动重登一次。
func (s *Service) Compare(ctx context.Context) (CompareResult, error) {
	if err := s.requireDesktop(); err != nil {
		return CompareResult{}, err
	}
	secret, err := s.linkSecret(ctx)
	if err != nil {
		return CompareResult{}, err
	}
	if secret.URL == "" || secret.Username == "" || secret.Password == "" {
		return CompareResult{}, ipc.NewError(ipc.CodeBadParam, "同步链接需要同时配置 URL、用户名和口令")
	}
	config := remoteConfigFromSecret(secret)
	for attempt := 0; ; attempt++ {
		session, err := s.engine.ensureSession(ctx, config)
		if err != nil {
			return CompareResult{}, err
		}
		result, compareErr := s.compareWithSession(ctx, session)
		if errors.Is(compareErr, errSessionExpired) && attempt == 0 {
			s.engine.dropSession(session.key)
			continue
		}
		return result, compareErr
	}
}

func (s *Service) compareWithSession(ctx context.Context, session *remoteSession) (CompareResult, error) {
	optIn, err := s.engine.loadKindOptIn(ctx, session.userID)
	if err != nil {
		return CompareResult{}, err
	}
	report := &SyncReport{}
	collected, err := s.engine.collectLocalObjects(ctx, report, optIn, session.userID)
	if err != nil {
		return CompareResult{}, err
	}
	local := make(map[string]compareEntity, len(collected))
	for id, object := range collected {
		local[id] = newCompareEntity(id, object.kind, object.plaintext)
	}
	idsResponse, err := session.client.ids(ctx)
	if err != nil {
		return CompareResult{}, err
	}
	remote, undecryptable, missed, err := s.fetchRemoteEntities(ctx, session, idsResponse, optIn)
	if err != nil {
		return CompareResult{}, err
	}
	if missed > 0 {
		report.warnf("%d 个云端对象未能拉取(超出单页预算), 对比结果不包含它们", missed)
	}
	// buildCompareRows 边匹配边收缩 remote/undecryptable, 云端对象数必须先取定。
	remoteCount := len(remote) + len(undecryptable)
	return CompareResult{
		Rows:     buildCompareRows(local, remote, undecryptable),
		Warnings: report.Warnings,
		Remote:   remoteCount,
	}, nil
}

const (
	comparePullBatch  = 64
	comparePullRounds = 4
)

// fetchRemoteEntities 拉取并解密全部云端对象; opt-in 硬关闭的种类(含其墓碑)不参与对比,
// 与同步轮的应用过滤一致。拉取按 ID 补拉(sinceSeq 取 maxSeq, 游标分支留空, 预算全给补拉),
// 超出单页预算被截断的对象有界重试; 解密失败的记入 undecryptable, 由调用方如实成行。
func (s *Service) fetchRemoteEntities(ctx context.Context, session *remoteSession, idsResponse *IDsResponse, optIn kindOptIn) (remote map[string]compareEntity, undecryptable map[string]bool, missed int, returnErr error) {
	remote = map[string]compareEntity{}
	undecryptable = map[string]bool{}
	pending := idsResponse.Entries
	for round := 0; round < comparePullRounds && len(pending) > 0; round++ {
		next := []IDEntry{}
		for offset := 0; offset < len(pending); offset += comparePullBatch {
			batch := pending[offset:min(offset+comparePullBatch, len(pending))]
			lookup := make([]string, 0, len(batch))
			for _, entry := range batch {
				lookup = append(lookup, entry.ID)
			}
			response, err := session.client.pull(ctx, idsResponse.MaxSeq, lookup, maxPullBytes)
			if err != nil {
				return nil, nil, 0, err
			}
			objects := make(map[string]WireObjectSeq, len(response.Objects))
			for _, object := range response.Objects {
				objects[object.ID] = object
			}
			for _, entry := range batch {
				wire, found := objects[entry.ID]
				if !found {
					next = append(next, entry)
					continue
				}
				kind, plaintext, err := tryOpenObject(session.dek, wire.Blob, entry.ID)
				if err != nil {
					undecryptable[entry.ID] = true
					continue
				}
				if !optIn.allowsRemote(kind, plaintext) {
					continue
				}
				remote[entry.ID] = newCompareEntity(entry.ID, kind, plaintext)
			}
		}
		pending = next
	}
	return remote, undecryptable, len(pending), nil
}
