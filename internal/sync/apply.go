package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

const CommandApplyObjects = "sync_apply_objects"

const (
	// maxApplyObjectsPerCall 单次应用对象数上限, 超出由调用方分批。
	maxApplyObjectsPerCall = 256
	// maxApplyObjectBytes 单对象明文上限, 与 maxSyncObjectBytes 对齐(64MiB 会话内容 + JSON/base64 余量)。
	maxApplyObjectBytes = maxSyncObjectBytes
)

const (
	ApplyResultApplied   = "applied"
	ApplyResultIdentical = "identical"
	ApplyResultSkipped   = "skipped"
)

// ApplyObject 是浏览器已解密的一个同步对象; 服务端只按明文应用, 不接收口令或 DEK。
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
	return s.engine.applyPlainObjects(ctx, request)
}

func (e *Engine) applyPlainObjects(ctx context.Context, request ApplyObjectsRequest) (ApplyObjectsResult, error) {
	if len(request.Objects) > maxApplyObjectsPerCall {
		return ApplyObjectsResult{}, ipc.NewError(ipc.CodeBadParam,
			fmt.Sprintf("单次应用对象数超过上限 %d", maxApplyObjectsPerCall))
	}
	result := ApplyObjectsResult{Objects: make([]ApplyObjectResult, 0, len(request.Objects))}
	for _, object := range request.Objects {
		entry := e.applyPlainObject(ctx, object)
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

// applyPlainObject 校验并应用单个明文对象; 校验失败隔离为该对象的 skipped + 警告, 不阻断整批。
func (e *Engine) applyPlainObject(ctx context.Context, object ApplyObject) ApplyObjectResult {
	entry := ApplyObjectResult{ID: object.ID, Kind: object.Kind, Result: ApplyResultSkipped}
	reject := func(format string, args ...any) ApplyObjectResult {
		entry.Warning = fmt.Sprintf(format, args...)
		return entry
	}
	if err := store.EnsureID(object.ID); err != nil {
		return reject("%v", err)
	}
	switch object.Kind {
	case KindGroup, KindAsset, KindCredential, KindSnippet, KindTombstone, KindTranscript:
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
	applied, identical := e.applyDecryptedObject(ctx, object.ID, object.Kind, canonical, report)
	entry.Warning = strings.Join(report.Warnings, "; ")
	if applied {
		entry.Result = ApplyResultApplied
	} else if identical {
		entry.Result = ApplyResultIdentical
	}
	return entry
}

// canonicalApplyPayload 把传输载荷严格解码到对应协议结构并重新编码为规范 JSON:
// identical 字节判定与 equal-revision LWW 哈希都以规范载荷为准, 与来源设备的编码无关。
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
	}
	return ""
}

// applyDecryptedObject 按种类应用一个已解密对象, 拉取合并与浏览器明文应用共用同一套 apply 语义。
func (e *Engine) applyDecryptedObject(ctx context.Context, objectID, kind string, plaintext []byte, report *SyncReport) (bool, bool) {
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
		return e.applyTombstoneObject(ctx, objectID, plaintext, report)
	case KindTranscript:
		return e.applyTranscriptObject(ctx, plaintext, report)
	}
	return false, false
}
