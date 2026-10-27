package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const CommandApplyObjects = "sync_apply_objects"

const (
	// maxApplyObjectsPerCall caps objects per apply call; callers batch beyond it.
	maxApplyObjectsPerCall = 256
	// maxApplyObjectBytes caps a single object's plaintext, aligned with maxSyncObjectBytes (64MiB transcript content + JSON/base64 headroom).
	maxApplyObjectBytes = maxSyncObjectBytes
)

const (
	ApplyResultApplied   = "applied"
	ApplyResultIdentical = "identical"
	ApplyResultSkipped   = "skipped"
)

// ApplyObject is one sync object already decrypted by the browser; the server applies plaintext only and never receives passwords or DEKs.
type ApplyObject struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type ApplyObjectsRequest struct {
	Objects []ApplyObject `json:"objects"`
}

type ApplyObjectResult struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Result  string `json:"result"`
	Warning string `json:"warning,omitempty"`
}

type ApplyObjectsResult struct {
	Applied   int                 `json:"applied"`
	Identical int                 `json:"identical"`
	Skipped   int                 `json:"skipped"`
	Objects   []ApplyObjectResult `json:"objects"`
}

func (s *Service) ApplyObjects(ctx context.Context, request ApplyObjectsRequest) (ApplyObjectsResult, error) {
	userID, _ := UserIDFromContext(ctx)
	result, err := s.engine.applyPlainObjects(ctx, request, userID)
	if err != nil {
		s.engine.logger.Warn("sync apply failed", "user", userID, "received", len(request.Objects), "error", err)
		return result, err
	}
	if len(request.Objects) > 0 {
		attrs := []any{"user", userID, "received", len(request.Objects),
			"applied", result.Applied, "identical", result.Identical, "skipped", result.Skipped}
		if result.Skipped > 0 {
			s.engine.logger.Warn("sync apply", append(attrs, "kinds", skippedApplyKinds(result))...)
		} else {
			s.engine.logger.Info("sync apply", attrs...)
		}
	}
	if result.Applied > 0 {
		if s.profilesReload != nil {
			if err := s.profilesReload(ctx); err != nil {
				s.engine.logger.Warn("reload synced AI profiles failed", "error", err)
			}
		}
		if s.permissionsReload != nil {
			if err := s.permissionsReload(ctx); err != nil {
				s.engine.logger.Warn("reload synced AI permission failed", "error", err)
			}
		}
		if s.grantsReload != nil {
			if err := s.grantsReload(ctx); err != nil {
				s.engine.logger.Warn("reload synced AI grants failed", "error", err)
			}
		}
		_ = ipc.Emit(ctx, s.events, ipc.TopicSyncStatus, ipc.SyncStatusEvent{OwnerID: userID})
	}
	return result, nil
}

func skippedApplyKinds(result ApplyObjectsResult) map[string]int {
	kinds := map[string]int{}
	for _, object := range result.Objects {
		if object.Result == ApplyResultSkipped {
			kinds[object.Kind]++
		}
	}
	return kinds
}

func (e *Engine) applyPlainObjects(ctx context.Context, request ApplyObjectsRequest, userIDs ...string) (ApplyObjectsResult, error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	if len(request.Objects) > maxApplyObjectsPerCall {
		return ApplyObjectsResult{}, ipc.NewError(ipc.CodeBadParam,
			fmt.Sprintf("单次应用对象数超过上限 %d", maxApplyObjectsPerCall))
	}
	if err := e.preflightAIObjects(ctx, request.Objects); err != nil {
		return ApplyObjectsResult{}, err
	}
	result := ApplyObjectsResult{Objects: make([]ApplyObjectResult, len(request.Objects))}
	for _, index := range applyObjectOrder(request.Objects) {
		object := request.Objects[index]
		entry := e.applyPlainObject(ctx, object, userIDs...)
		switch entry.Result {
		case ApplyResultApplied:
			result.Applied++
		case ApplyResultIdentical:
			result.Identical++
		default:
			result.Skipped++
		}
		result.Objects[index] = entry
	}
	return result, nil
}

func applyObjectOrder(objects []ApplyObject) []int {
	indices := make([]int, len(objects))
	groups := make(map[string]int)
	for index, object := range objects {
		indices[index] = index
		if object.Kind == KindGroup {
			if _, exists := groups[object.ID]; !exists {
				groups[object.ID] = index
			}
		}
	}
	sort.SliceStable(indices, func(left, right int) bool {
		return objectKindRank(objects[indices[left]].Kind) < objectKindRank(objects[indices[right]].Kind)
	})
	ordered := make([]int, 0, len(indices))
	visited := make(map[int]bool)
	var visit func(int)
	visit = func(index int) {
		if visited[index] {
			return
		}
		visited[index] = true
		if objects[index].Kind == KindGroup {
			var group groupObject
			if json.Unmarshal(objects[index].Payload, &group) == nil && group.ParentID != nil {
				if parent, found := groups[*group.ParentID]; found {
					visit(parent)
				}
			}
		}
		ordered = append(ordered, index)
	}
	for _, index := range indices {
		visit(index)
	}
	return ordered
}

// applyPlainObject validates and applies a single plaintext object; validation failures are isolated as that object's skipped + warning and do not block the whole batch.
func (e *Engine) applyPlainObject(ctx context.Context, object ApplyObject, userIDs ...string) ApplyObjectResult {
	entry := ApplyObjectResult{ID: object.ID, Kind: object.Kind, Result: ApplyResultSkipped}
	reject := func(format string, args ...any) ApplyObjectResult {
		entry.Warning = fmt.Sprintf(format, args...)
		return entry
	}
	spec, ok := lookupKind(object.Kind)
	if !ok {
		return reject("不支持的同步对象种类 %q", object.Kind)
	}
	if spec.strictID {
		if err := store.EnsureID(object.ID); err != nil {
			return reject("%v", err)
		}
	} else if !validObjectID(object.ID) {
		return reject("%v", ipc.NewError(ipc.CodeBadParam, "同步对象 ID 不合法"))
	}
	if len(object.Payload) == 0 {
		return reject("对象数据为空")
	}
	if len(object.Payload) > maxApplyObjectBytes {
		return reject("对象数据超过大小上限 %d 字节", int64(maxApplyObjectBytes))
	}
	canonical, err := canonicalApplyPayload(object)
	if err != nil {
		return reject("%v", err)
	}
	if spec.validateApply != nil {
		if warning := spec.validateApply(e, ctx, canonical); warning != "" {
			return reject("%s", warning)
		}
	}
	report := &SyncReport{}
	applied, identical := e.applyDecryptedObject(ctx, object.ID, object.Kind, canonical, report, userIDs...)
	entry.Warning = strings.Join(report.Warnings, "; ")
	if applied {
		entry.Result = ApplyResultApplied
	} else if identical {
		entry.Result = ApplyResultIdentical
	}
	return entry
}

// canonicalApplyPayload strictly decodes the transport payload into the corresponding protocol struct and re-encodes it as canonical JSON:
// identical byte decisions and equal-revision LWW hashes are based on the canonical payload, independent of the source device's encoding.
func canonicalApplyPayload(object ApplyObject) ([]byte, error) {
	spec, ok := lookupKind(object.Kind)
	if !ok {
		return nil, ipc.NewError(ipc.CodeBadParam, "不支持的同步对象种类")
	}
	decoded, err := spec.newPayload(object.ID)
	if err != nil {
		return nil, err
	}
	if !json.Valid(object.Payload) {
		return nil, ipc.NewError(ipc.CodeBadParam, "对象数据不是合法 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(object.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(decoded); err != nil {
		return nil, ipc.WrapError(ipc.CodeBadParam, "对象数据与对象种类不匹配", err)
	}
	if spec.idInPayload {
		identified, ok := decoded.(identifiedPayload)
		if !ok || identified.payloadID() != object.ID {
			return nil, ipc.NewError(ipc.CodeBadParam, "对象数据 ID 与对象 ID 不一致")
		}
	}
	return marshalObject(decoded)
}

func firstUserID(ids []string) string {
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// applyDecryptedObject applies one decrypted object by kind; pull-merge and browser plaintext application share the same apply semantics.
func (e *Engine) applyDecryptedObject(ctx context.Context, objectID, kind string, plaintext []byte, report *SyncReport, userIDs ...string) (bool, bool) {
	spec, ok := lookupKind(kind)
	if !ok {
		return false, false
	}
	return spec.apply(e, ctx, objectID, plaintext, report, firstUserID(userIDs))
}

// validateGroupApply rejects a browser-applied group whose parent is itself or has not synced yet.
func (e *Engine) validateGroupApply(ctx context.Context, canonical []byte) string {
	var group groupObject
	if err := json.Unmarshal(canonical, &group); err != nil {
		return err.Error()
	}
	if group.ParentID == nil || *group.ParentID == "" {
		return ""
	}
	if *group.ParentID == group.ID {
		return "分组不能将自身设为父级"
	}
	if _, err := e.store.GroupGet(ctx, *group.ParentID); isNotFound(err) {
		tombstone, found, tombstoneErr := e.syncTombstoneGet(ctx, *group.ParentID)
		if tombstoneErr != nil {
			return fmt.Sprintf("无法检查父级删除标记：%v", tombstoneErr)
		}
		if !found || tombstone.Kind != KindGroup || tombstone.DeletedAt < group.UpdatedAt {
			return fmt.Sprintf("父级分组 %s 尚未同步，稍后重试", *group.ParentID)
		}
	} else if err != nil {
		return fmt.Sprintf("无法检查父级分组：%v", err)
	}
	return ""
}
