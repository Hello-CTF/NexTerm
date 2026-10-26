package sync

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// Sync object kinds. Tombstones share the object id of their target, overwrite it in storage, and are decided by client-side LWW.
const (
	KindGroup        = "group"
	KindAsset        = "asset"
	KindCredential   = "credential"
	KindSnippet      = "snippet"
	KindTombstone    = "tombstone"
	KindTranscript   = "transcript"
	KindKnownHost    = "known_host"
	KindAIProfile    = "ai_profile"
	KindAIPermission = "ai_permission"
	KindAIGrant      = "ai_grant"
	KindPreference   = "preference"
)

// objectKinds is the fixed enumeration order for decryption attempts; AAD binds kind, so a wrong kind can never decrypt.
var objectKinds = []string{KindGroup, KindAsset, KindCredential, KindSnippet, KindTombstone, KindTranscript, KindKnownHost, KindAIProfile, KindAIPermission, KindAIGrant, KindPreference}

const (
	objectAADPrefix = "nexterm/go/sync-object/v1"
	objectNonceSize = 12
)

// maxSyncObjectBytes caps a single object's ciphertext (64MiB content + JSON/base64 headroom).
const maxSyncObjectBytes = 96 << 20

func objectAAD(id, kind string) []byte {
	return []byte(objectAADPrefix + "\x00" + id + "\x00" + kind)
}

func sealObject(dek, plaintext []byte, id, kind string) ([]byte, error) {
	if len(dek) != 32 {
		return nil, ipc.NewError(ipc.CodeCrypto, "加密错误: 数据密钥长度不合法")
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: "+err.Error(), err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: "+err.Error(), err)
	}
	nonce := make([]byte, objectNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: 随机数生成失败", err)
	}
	return aead.Seal(nonce, nonce, plaintext, objectAAD(id, kind)), nil
}

func openObject(dek, blob []byte, id, kind string) ([]byte, error) {
	if len(dek) != 32 {
		return nil, ipc.NewError(ipc.CodeCrypto, "加密错误: 数据密钥长度不合法")
	}
	if len(blob) <= objectNonceSize {
		return nil, ipc.NewError(ipc.CodeDecrypt, "凭据解密失败: 同步对象长度不合法")
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: "+err.Error(), err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: "+err.Error(), err)
	}
	plaintext, err := aead.Open(nil, blob[:objectNonceSize], blob[objectNonceSize:], objectAAD(id, kind))
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeDecrypt, "凭据解密失败（密钥不匹配或数据损坏）", err)
	}
	return plaintext, nil
}

// tryOpenObject attempts decryption with the fixed kind enumeration; if all fail, the key does not match or the data was tampered with.
func tryOpenObject(dek, blob []byte, id string) (string, []byte, error) {
	for _, kind := range objectKinds {
		plaintext, err := openObject(dek, blob, id, kind)
		if err == nil {
			return kind, plaintext, nil
		}
	}
	if plaintext, matched, err := openCompressedTranscript(dek, blob, id); matched {
		if err != nil {
			return "", nil, err
		}
		return KindTranscript, plaintext, nil
	}
	return "", nil, ipc.NewError(ipc.CodeDecrypt, "凭据解密失败（密钥不匹配或数据损坏）")
}

type groupObject struct {
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId,omitempty"`
	Name      string  `json:"name"`
	Sort      int64   `json:"sort"`
	CreatedAt int64   `json:"createdAt"`
	UpdatedAt int64   `json:"updatedAt"`
}

type assetObject struct {
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

type credentialObject struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Secret    string `json:"secret"`
	UpdatedAt int64  `json:"updatedAt"`
}

type snippetObject struct {
	ID        string  `json:"id"`
	GroupID   *string `json:"groupId,omitempty"`
	Name      string  `json:"name"`
	Body      string  `json:"body"`
	Sort      int64   `json:"sort"`
	CreatedAt int64   `json:"createdAt"`
	UpdatedAt int64   `json:"updatedAt"`
}

type tombstoneObject struct {
	TargetKind string `json:"targetKind"`
	DeletedAt  int64  `json:"deletedAt"`
	// Conflict tombstones (known_host only) carry the original loser triple, distinguishing them from user-initiated deletion tombstones:
	// the absorbing side uses this to recognize a winning incarnation with the same ID but a different triple and safely re-ID it instead of clearing it as a user deletion.
	Host    string `json:"host,omitempty"`
	Port    int32  `json:"port,omitempty"`
	KeyType string `json:"keyType,omitempty"`
}

// knownHostObject's revision is addedAt (re-accepting a host key refreshes it); the (host, port, keyType) triple is globally unique.
// The payload is fingerprint-only by contract: it never carries key material. A sync
// apply that changes the fingerprint clears the locally stored key material (the row
// degrades to fingerprint-only trust until the next verified accept refills it through
// KnownHostFillKeyMaterial or PutHostKey); a same-fingerprint update preserves it.
type knownHostObject struct {
	ID          string `json:"id"`
	Host        string `json:"host"`
	Port        int32  `json:"port"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
	AddedAt     int64  `json:"addedAt"`
}

// aiProfileObject fields correspond one-to-one with the internal/ai/profiles.Profile persisted form, plus the updatedAt revision.
// apiKey is always plaintext in the payload (the whole payload is E2E-encrypted with the DEK) and must become an enc:v1: envelope before persisting.
type aiProfileObject struct {
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

	UpdatedAt int64 `json:"updatedAt"`
}

type aiPermissionObject struct {
	ID          string   `json:"id"`
	Mode        string   `json:"mode"`
	DangerRules []string `json:"dangerRules"`
	UpdatedAt   int64    `json:"updatedAt"`
}

// AI grant objects share the ai_grant kind, split into three families by object ID prefix:
// device/<deviceID> (device-level grants), rule/<ruleID> (path-level grant rules), mode/<deviceID> (per-device permission modes).
// Fields align with the guard persisted forms (guard.DeviceGrant / guard.GrantRule / the ai.device_permission map values);
// updatedAt is the LWW revision, revocations converge via tombstones sharing the object ID.
type aiGrantDeviceObject struct {
	ID        string   `json:"id"`
	DeviceID  string   `json:"deviceId"`
	Kinds     []string `json:"kinds"`
	UpdatedAt int64    `json:"updatedAt"`
}

type aiGrantRuleObject struct {
	ID        string `json:"id"`
	DeviceID  string `json:"deviceId"`
	Action    string `json:"action"`
	Path      string `json:"path,omitempty"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

type aiGrantModeObject struct {
	ID        string `json:"id"`
	DeviceID  string `json:"deviceId"`
	Mode      string `json:"mode"`
	UpdatedAt int64  `json:"updatedAt"`
}

type preferenceObject struct {
	ID        string          `json:"id"`
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	UpdatedAt int64           `json:"updatedAt"`
}

type transcriptChunkObject struct {
	Seq   int64  `json:"seq"`
	TabID string `json:"tabId"`
	TS    int64  `json:"ts"`
	Kind  int    `json:"kind,omitempty"`
	Data  []byte `json:"data"`
}

type transcriptObject struct {
	ID             string                  `json:"id"`
	SessionID      string                  `json:"sessionId,omitempty"`
	AssetID        string                  `json:"assetId"`
	AssetName      string                  `json:"assetName"`
	AssetKind      string                  `json:"assetKind"`
	StartedAt      int64                   `json:"startedAt"`
	EndedAt        int64                   `json:"endedAt"`
	Bytes          int64                   `json:"bytes"`
	Chunks         int64                   `json:"chunks"`
	Truncated      bool                    `json:"truncated"`
	ContentOmitted bool                    `json:"contentOmitted,omitempty"`
	Content        []transcriptChunkObject `json:"content,omitempty"`
}

// marshalObject produces deterministic JSON as the payload plaintext; the same content always yields the same bytes, for LWW tiebreaks and incremental reconciliation.
func marshalObject(payload any) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeInternal, "无法编码同步对象", err)
	}
	return encoded, nil
}

func objectPayloadHash(plaintext []byte) string {
	digest := sha256.Sum256(plaintext)
	return hex.EncodeToString(digest[:])
}
