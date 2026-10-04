package store

const (
	BuiltinLocalAssetID   = "01J0NEXTERMLOCALDEVICE0001"
	BuiltinLocalAssetName = "当前设备"
	LayoutKey             = "layout.workspace"
)

type AssetGroupRow struct {
	ID        string  `json:"id"`
	ParentID  *string `json:"parent_id"`
	Name      string  `json:"name"`
	Sort      int64   `json:"sort"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

type AssetRow struct {
	ID          string  `json:"id"`
	GroupID     *string `json:"groupId"`
	Kind        string  `json:"kind"`
	Name        string  `json:"name"`
	Host        *string `json:"host"`
	Port        *int32  `json:"port"`
	Username    *string `json:"username"`
	AuthKind    *string `json:"authKind"`
	KeyPath     *string `json:"keyPath"`
	CredID      *string `json:"credId"`
	OptionsJSON string  `json:"optionsJson"`
	Tags        string  `json:"tags"`
	Note        string  `json:"note"`
	Sort        int64   `json:"sort"`
	CreatedAt   int64   `json:"createdAt"`
	UpdatedAt   int64   `json:"updatedAt"`
	DeletedAt   *int64  `json:"deletedAt"`
	Builtin     bool    `json:"builtin"`
}

type CredentialRow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Cipher    string `json:"cipher"`
	Nonce     []byte `json:"-"`
	Blob      []byte `json:"-"`
	KEKHint   string `json:"kekHint"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

type CredentialMeta struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Cipher    string `json:"cipher"`
	KEKHint   string `json:"kekHint"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

func (r CredentialRow) Meta() CredentialMeta {
	return CredentialMeta{
		ID: r.ID, Name: r.Name, Kind: r.Kind, Cipher: r.Cipher,
		KEKHint: r.KEKHint, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

type AuditRow struct {
	ID          int64   `json:"id"`
	TS          int64   `json:"ts"`
	SessionID   *string `json:"sessionId"`
	AssetID     *string `json:"assetId"`
	Source      string  `json:"source"`
	Kind        string  `json:"kind"`
	PayloadJSON string  `json:"payloadJson"`
	ExitCode    *int32  `json:"exitCode"`
	DurationMS  *int64  `json:"durationMs"`
}

type ConversationRow struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	ScopeJSON string `json:"scopeJson"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

type MessageRow struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationId"`
	Role           string `json:"role"`
	ContentJSON    string `json:"contentJson"`
	TokensIn       *int64 `json:"tokensIn"`
	TokensOut      *int64 `json:"tokensOut"`
	CreatedAt      int64  `json:"createdAt"`
}

type SnippetRow struct {
	ID        string  `json:"id"`
	GroupID   *string `json:"group_id"`
	Name      string  `json:"name"`
	Body      string  `json:"body"`
	Sort      int64   `json:"sort"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

type KnownHostRow struct {
	ID          string `json:"id"`
	Host        string `json:"host"`
	Port        int32  `json:"port"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
	AddedAt     int64  `json:"addedAt"`
}

type RecordingRow struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	TabID     string `json:"tab_id"`
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	StartedAt int64  `json:"started_at"`
	EndedAt   *int64 `json:"ended_at"`
}

type AssetRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type GroupInput struct {
	ParentID *string
	Name     string
	Sort     int64
}

type AssetInput struct {
	GroupID     *string
	Kind        string
	Name        string
	Host        *string
	Port        *int32
	Username    *string
	AuthKind    *string
	KeyPath     *string
	CredID      *string
	OptionsJSON string
	Tags        string
	Note        string
	Sort        int64
}

type Optional[T any] struct {
	Set   bool
	Value *T
}

func Value[T any](value T) Optional[T] {
	return Optional[T]{Set: true, Value: &value}
}

func Null[T any]() Optional[T] {
	return Optional[T]{Set: true}
}

type GroupPatch struct {
	Name     *string
	ParentID Optional[string]
	Sort     *int64
}

type AssetPatch struct {
	GroupID     Optional[string]
	Name        *string
	Host        Optional[string]
	Port        Optional[int32]
	Username    Optional[string]
	AuthKind    Optional[string]
	KeyPath     Optional[string]
	CredID      Optional[string]
	OptionsJSON *string
	Tags        *string
	Note        *string
	Sort        *int64
}

type CredentialInput struct {
	ID      string
	Name    string
	Kind    string
	Nonce   []byte
	Blob    []byte
	KEKHint string
}

type AuditQuery struct {
	SessionID *string
	AssetID   *string
	Source    *string
	Kind      *string
	Limit     int64
	Offset    int64
}

type AuditInput struct {
	SessionID  *string
	AssetID    *string
	Source     string
	Kind       string
	Payload    any
	ExitCode   *int32
	DurationMS *int64
}
