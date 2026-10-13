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

// Row states match the browser CompareConsole RowState;
// remote-undecryptable is desktop-only: cloud objects that cannot be decrypted with the current DEK still surface as rows, not errors.
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

// CompareRow is one local-vs-remote object comparison; the revision is the LWW clock value, 0 means that side does not exist or is unreadable.
type CompareRow struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	State         string `json:"state"`
	UpdatedLocal  int64  `json:"updatedAtLocal,omitempty"`
	UpdatedRemote int64  `json:"updatedAtRemote,omitempty"`
}

// CompareResult is the sync_compare return: rows are sorted "differences first, then by name" (same as the browser),
// warnings carry local collection and remote pull alerts, remote is the number of cloud objects compared this round.
type CompareResult struct {
	Rows     []CompareRow `json:"rows"`
	Warnings []string     `json:"warnings,omitempty"`
	Remote   int          `json:"remote"`
}

// compareEntity is the decision view of one side's entity: plaintext is used only for hash/revision/name and never leaves this process.
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

// compareName extracts the row name from plaintext: groups/assets/snippets/credentials/AI profiles use name, known_host uses host:port,
// preferences use key, AI permissions are a global singleton, session records carry the asset name, tombstones degrade to the object id.
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

// compareDeleted decides whether this side's content is in deleted form: a tombstone object or a soft-deleted asset with deletedAt.
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

// compareLocalWins applies the browser localWins decision: same hash needs no push; a full-content session record beats the omitted-content version;
// otherwise the higher revision wins, ties break by payload hash lexicographic order.
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

// compareState mirrors the browser buildRows: same hash means identical; with a deleted form involved, the other side winning yields "deleted",
// this side winning yields "newer"; otherwise the LWW winner yields "newer".
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

// buildCompareRows merges local and remote entities into comparison rows: differences first (same last), then by name lexicographically.
// undecryptable is the set of undecryptable cloud object ids: local same-name rows are labeled with the local name, remote-only rows degrade to the id.
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

// Compare outputs a row-by-row local-vs-remote object comparison (desktop only): the local side is collected via the same collectLocalObjects
// as a sync round, the remote side pulls all objects and decrypts them with the engine session DEK; decision rules match the browser CompareConsole.
// The comparison is a read-only view: it advances no cursor and writes no reconciliation state. An expired session triggers one automatic re-login.
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
	// buildCompareRows shrinks remote/undecryptable as it matches, so the cloud object count must be taken first.
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

// fetchRemoteEntities pulls and decrypts all cloud objects; kinds with opt-in hard-off (including their tombstones) do not participate,
// matching the sync round's apply filter. The pull is by-ID catch-up (sinceSeq at maxSeq, cursor branch left empty, budget all to catch-up);
// objects truncated by the single-page budget get bounded retries; decryption failures go into undecryptable and surface as rows via the caller.
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
