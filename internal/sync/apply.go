package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
		_ = ipc.Emit(ctx, s.events, ipc.TopicSyncStatus, struct{}{})
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
	result := ApplyObjectsResult{Objects: make([]ApplyObjectResult, 0, len(request.Objects))}
	for _, object := range request.Objects {
		entry := e.applyPlainObject(ctx, object, userIDs...)
		switch entry.Result {
		case ApplyResultApplied:
			result.Applied++
		case ApplyResultIdentical:
			result.Identical++
		default:
			result.Skipped++
		}
		result.Objects = append(result.Objects, entry)
	}
	return result, nil
}

// applyPlainObject validates and applies a single plaintext object; validation failures are isolated as that object's skipped + warning and do not block the whole batch.
func (e *Engine) applyPlainObject(ctx context.Context, object ApplyObject, userIDs ...string) ApplyObjectResult {
	entry := ApplyObjectResult{ID: object.ID, Kind: object.Kind, Result: ApplyResultSkipped}
	reject := func(format string, args ...any) ApplyObjectResult {
		entry.Warning = fmt.Sprintf(format, args...)
		return entry
	}
	switch object.Kind {
	case KindGroup, KindAsset, KindCredential, KindSnippet, KindTranscript, KindKnownHost, KindAIProfile:
		if err := store.EnsureID(object.ID); err != nil {
			return reject("%v", err)
		}
	case KindTombstone, KindAIPermission, KindAIGrant, KindPreference:
		if !validObjectID(object.ID) {
			return reject("%v", ipc.NewError(ipc.CodeBadParam, "同步对象 ID 不合法"))
		}
	default:
		return reject("不支持的同步对象种类 %q", object.Kind)
	}
	if len(object.Payload) == 0 {
		return reject("对象载荷为空")
	}
	if len(object.Payload) > maxApplyObjectBytes {
		return reject("对象载荷超过大小上限 %d 字节", int64(maxApplyObjectBytes))
	}
	canonical, err := canonicalApplyPayload(object)
	if err != nil {
		return reject("%v", err)
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
	var decoded any
	switch object.Kind {
	case KindGroup:
		decoded = &groupObject{}
	case KindAsset:
		decoded = &assetObject{}
	case KindCredential:
		decoded = &credentialObject{}
	case KindSnippet:
		decoded = &snippetObject{}
	case KindTombstone:
		decoded = &tombstoneObject{}
	case KindTranscript:
		decoded = &transcriptObject{}
	case KindKnownHost:
		decoded = &knownHostObject{}
	case KindAIProfile:
		decoded = &aiProfileObject{}
	case KindAIPermission:
		decoded = &aiPermissionObject{}
	case KindAIGrant:
		switch {
		case strings.HasPrefix(object.ID, aiGrantDevicePrefix):
			decoded = &aiGrantDeviceObject{}
		case strings.HasPrefix(object.ID, aiGrantRulePrefix):
			decoded = &aiGrantRuleObject{}
		case strings.HasPrefix(object.ID, aiGrantModePrefix):
			decoded = &aiGrantModeObject{}
		default:
			return nil, ipc.NewError(ipc.CodeBadParam, "AI 授权对象 ID 不受支持")
		}
	case KindPreference:
		decoded = &preferenceObject{}
	default:
		return nil, ipc.NewError(ipc.CodeBadParam, "不支持的同步对象种类")
	}
	if !json.Valid(object.Payload) {
		return nil, ipc.NewError(ipc.CodeBadParam, "对象载荷不是合法 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(object.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(decoded); err != nil {
		return nil, ipc.WrapError(ipc.CodeBadParam, "对象载荷与对象种类不匹配", err)
	}
	if object.Kind != KindTombstone && applyPayloadID(decoded) != object.ID {
		return nil, ipc.NewError(ipc.CodeBadParam, "对象载荷 ID 与对象 ID 不一致")
	}
	return marshalObject(decoded)
}

func applyPayloadID(decoded any) string {
	switch payload := decoded.(type) {
	case *groupObject:
		return payload.ID
	case *assetObject:
		return payload.ID
	case *credentialObject:
		return payload.ID
	case *snippetObject:
		return payload.ID
	case *transcriptObject:
		return payload.ID
	case *knownHostObject:
		return payload.ID
	case *aiProfileObject:
		return payload.ID
	case *aiPermissionObject:
		return payload.ID
	case *aiGrantDeviceObject:
		return payload.ID
	case *aiGrantRuleObject:
		return payload.ID
	case *aiGrantModeObject:
		return payload.ID
	case *preferenceObject:
		return payload.ID
	}
	return ""
}

func firstUserID(ids []string) string {
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// applyDecryptedObject applies one decrypted object by kind; pull-merge and browser plaintext application share the same apply semantics.
func (e *Engine) applyDecryptedObject(ctx context.Context, objectID, kind string, plaintext []byte, report *SyncReport, userIDs ...string) (bool, bool) {
	switch kind {
	case KindGroup:
		return e.applyGroupObject(ctx, plaintext, report)
	case KindAsset:
		return e.applyAssetObject(ctx, plaintext, report)
	case KindCredential:
		return e.applyCredentialObject(ctx, plaintext, report)
	case KindSnippet:
		return e.applySnippetObject(ctx, plaintext, report)
	case KindTombstone:
		return e.applyTombstoneObject(ctx, objectID, plaintext, report, userIDs...)
	case KindTranscript:
		return e.applyTranscriptObject(ctx, plaintext, report)
	case KindKnownHost:
		return e.applyKnownHostObject(ctx, plaintext, report)
	case KindAIProfile:
		return e.applyAIProfileObject(ctx, plaintext, report)
	case KindAIPermission:
		return e.applyAIPermissionObject(ctx, plaintext, report)
	case KindAIGrant:
		return e.applyAIGrantObject(ctx, objectID, plaintext, report)
	case KindPreference:
		return e.applyPreferenceObject(ctx, plaintext, report, firstUserID(userIDs))
	}
	return false, false
}
