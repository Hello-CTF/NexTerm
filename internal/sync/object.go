package sync

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

// ProtocolVersion 2: 全量载荷 E2E 对象协议。破坏性替换 v1, 不做旧版本兼容。
const ProtocolVersion = 2

// 同步对象种类。tombstone 与目标对象共用同一对象 id, 覆盖存储, 由客户端 LWW 裁决。
const (
	KindGroup      = "group"
	KindAsset      = "asset"
	KindCredential = "credential"
	KindSnippet    = "snippet"
	KindTombstone  = "tombstone"
	KindTranscript = "transcript"
	KindKnownHost  = "known_host"
	KindAIProfile  = "ai_profile"
)

// objectKinds 是尝试解密时的固定枚举顺序; AAD 绑定 kind, 错误 kind 必然解不开。
var objectKinds = []string{KindGroup, KindAsset, KindCredential, KindSnippet, KindTombstone, KindTranscript, KindKnownHost, KindAIProfile}

const (
	objectAADPrefix = "nexterm/go/sync-object/v1"
	objectNonceSize = 12
)

// maxSyncObjectBytes 单对象密文上限(64MiB 内容 + JSON/base64 余量)。
const maxSyncObjectBytes = 96 << 20

func objectAAD(id, kind string) []byte {
	return []byte(objectAADPrefix + "\x00" + id + "\x00" + kind)
}

func sealObject(dek, plaintext []byte, id, kind string) ([]byte, error) {
	if len(dek) != 32 {
		return nil, ipc.NewError(ipc.CodeCrypto, "加密错误: DEK 长度不合法")
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
		return nil, ipc.NewError(ipc.CodeCrypto, "加密错误: DEK 长度不合法")
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

// tryOpenObject 用固定 kind 枚举尝试解密; 全部失败说明密钥不匹配或数据被篡改。
func tryOpenObject(dek, blob []byte, id string) (string, []byte, error) {
	for _, kind := range objectKinds {
		plaintext, err := openObject(dek, blob, id, kind)
		if err == nil {
			return kind, plaintext, nil
		}
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
	// 冲突墓碑(仅 known_host)携带原败者三元组, 与用户主动删除墓碑区分:
	// 吸收方据此识别同 ID 不同三元组的胜出化身并安全 re-ID, 而不是按用户删除清掉。
	Host    string `json:"host,omitempty"`
	Port    int32  `json:"port,omitempty"`
	KeyType string `json:"keyType,omitempty"`
}

// knownHostObject 的修订号即 addedAt(重新接受主机密钥会刷新); 三元组 (host, port, keyType) 全局唯一。
type knownHostObject struct {
	ID          string `json:"id"`
	Host        string `json:"host"`
	Port        int32  `json:"port"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
	AddedAt     int64  `json:"addedAt"`
}

// aiProfileObject 字段与 internal/ai/profiles.Profile 持久化形态一一对应, 另加修订号 updatedAt。
// apiKey 在载荷中恒为明文(整体经 DEK 端到端加密), 落盘前必须转为 enc:v1: 信封。
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

// marshalObject 产出确定性 JSON 作为载荷明文; 同一内容永远得到同一字节, 供 LWW 决胜与增量对账。
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
