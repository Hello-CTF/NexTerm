package upgrade_test

import "github.com/Hello-CTF/NexTerm/internal/store"

type releasedSchema struct {
	name  string
	mysql bool
	v103  bool
}

func releasedSchemas() []releasedSchema {
	return []releasedSchema{
		{name: "v1.0.3", v103: true},
		{name: "v1.0.7", v103: true},
		{name: "v1.1.1", mysql: true},
		{name: "v1.1.3", mysql: true},
	}
}

func (s releasedSchema) models() []any {
	if s.v103 {
		return []any{
			&store.AppUser{}, &store.Setting{}, &v103Asset{}, &v103Credential{}, &v103Conversation{},
			&v103Message{}, &v103Run{}, &v103Cron{}, &v103KnownHost{}, &v103SyncTombstone{},
			&v103CredentialTombstone{}, &v103UserDEK{}, &v103UserSyncObject{}, &v103UserSyncHead{},
			&v103SyncState{}, &v103UserTOTP{},
		}
	}
	return []any{
		&store.AppUser{}, &store.Setting{}, &store.AssetRow{}, &store.CredentialRow{}, &store.ConversationRow{},
		&store.MessageRow{}, &store.RunRow{}, &v111Cron{}, &v111KnownHost{}, &store.SyncTombstone{},
		&store.CredentialTombstone{}, &store.UserDEK{}, &store.UserSyncObject{}, &store.UserSyncHead{},
		&store.SyncState{}, &store.UserTOTP{},
	}
}

type v103Asset struct {
	ID          string  `gorm:"column:id;primaryKey"`
	GroupID     *string `gorm:"column:group_id;index:idx_asset_group"`
	Kind        string  `gorm:"column:kind;not null;index:idx_asset_kind"`
	Name        string  `gorm:"column:name;not null"`
	Host        *string `gorm:"column:host"`
	Port        *int32  `gorm:"column:port"`
	Username    *string `gorm:"column:username"`
	AuthKind    *string `gorm:"column:auth_kind"`
	KeyPath     *string `gorm:"column:key_path"`
	CredID      *string `gorm:"column:cred_id"`
	OptionsJSON string  `gorm:"column:options_json;not null;default:'{}'"`
	Tags        string  `gorm:"column:tags;not null;default:''"`
	Note        string  `gorm:"column:note;not null;default:''"`
	Sort        int64   `gorm:"column:sort;not null"`
	CreatedAt   int64   `gorm:"column:created_at;not null"`
	UpdatedAt   int64   `gorm:"column:updated_at;not null"`
	DeletedAt   *int64  `gorm:"column:deleted_at"`
	Builtin     bool    `gorm:"column:builtin;not null;default:0"`
	OwnerID     string  `gorm:"column:owner_id;not null;default:'';index:idx_asset_owner"`
}

func (v103Asset) TableName() string { return "asset" }

type v103Credential struct {
	ID        string `gorm:"column:id;primaryKey"`
	Name      string `gorm:"column:name;not null"`
	Kind      string `gorm:"column:kind;not null"`
	Cipher    string `gorm:"column:cipher;not null"`
	Nonce     []byte `gorm:"column:nonce;not null"`
	Blob      []byte `gorm:"column:blob;not null"`
	KEKHint   string `gorm:"column:kek_hint;not null"`
	CreatedAt int64  `gorm:"column:created_at;not null"`
	UpdatedAt int64  `gorm:"column:updated_at;not null"`
	OwnerID   string `gorm:"column:owner_id;not null;default:'';index:idx_credential_owner"`
}

func (v103Credential) TableName() string { return "credential" }

type v103Conversation struct {
	ID        string `gorm:"column:id;primaryKey"`
	Title     string `gorm:"column:title;not null;default:''"`
	ScopeJSON string `gorm:"column:scope_json;not null;default:'{}'"`
	CreatedAt int64  `gorm:"column:created_at;not null"`
	UpdatedAt int64  `gorm:"column:updated_at;not null"`
	OwnerID   string `gorm:"column:owner_id;not null;default:'';index:idx_ai_conversation_owner"`
}

func (v103Conversation) TableName() string { return "ai_conversation" }

type v103Message struct {
	ID             string `gorm:"column:id;primaryKey"`
	ConversationID string `gorm:"column:conversation_id;not null;uniqueIndex:idx_ai_msg_conv_seq,priority:1"`
	Role           string `gorm:"column:role;not null"`
	ContentJSON    string `gorm:"column:content_json;not null"`
	TokensIn       *int64 `gorm:"column:tokens_in"`
	TokensOut      *int64 `gorm:"column:tokens_out"`
	CreatedAt      int64  `gorm:"column:created_at;not null"`
	Seq            int64  `gorm:"column:seq;not null;default:0;uniqueIndex:idx_ai_msg_conv_seq,priority:2"`
	OwnerID        string `gorm:"column:owner_id;not null;default:'';index:idx_ai_message_owner"`
}

func (v103Message) TableName() string { return "ai_message" }

type v103Run struct {
	ID                  string `gorm:"column:id;primaryKey"`
	ConversationID      string `gorm:"column:conversation_id;not null;index:idx_ai_run_conv,priority:1"`
	Status              string `gorm:"column:status;not null;index:idx_ai_run_status"`
	Attempt             int64  `gorm:"column:attempt;not null;default:1"`
	Seq                 uint64 `gorm:"column:seq;not null;default:0"`
	PlanMode            bool   `gorm:"column:plan_mode;not null;default:0"`
	Source              string `gorm:"column:source;not null;default:'chat'"`
	ProfileID           string `gorm:"column:profile_id;not null;default:'';index:idx_ai_run_profile,priority:1"`
	Answer              string `gorm:"column:answer;not null;default:''"`
	Turns               int    `gorm:"column:turns;not null;default:0"`
	TokensIn            int64  `gorm:"column:tokens_in;not null;default:0"`
	TokensOut           int64  `gorm:"column:tokens_out;not null;default:0"`
	CacheCreationTokens int64  `gorm:"column:cache_creation_tokens;not null;default:0"`
	LatencyMS           int64  `gorm:"column:latency_ms;not null;default:0"`
	Error               string `gorm:"column:error;not null;default:''"`
	CreatedAt           int64  `gorm:"column:created_at;not null;index:idx_ai_run_conv,priority:2;index:idx_ai_run_profile,priority:2"`
	UpdatedAt           int64  `gorm:"column:updated_at;not null"`
	FinishedAt          *int64 `gorm:"column:finished_at"`
	OwnerID             string `gorm:"column:owner_id;not null;default:'';index:idx_ai_run_owner"`
}

func (v103Run) TableName() string { return "ai_run" }

type v103Cron struct {
	ID                  string `gorm:"column:id;primaryKey"`
	SessionID           string `gorm:"column:session_id;not null;index:idx_cron_job_session"`
	Name                string `gorm:"column:name;not null;default:''"`
	Prompt              string `gorm:"column:prompt;not null"`
	Schedule            string `gorm:"column:schedule;not null"`
	Timezone            string `gorm:"column:timezone;not null"`
	Enabled             bool   `gorm:"column:enabled;not null;check:chk_cron_enabled,enabled IN (false,true)"`
	TimeoutMS           int64  `gorm:"column:timeout_ms;not null;check:chk_cron_timeout,timeout_ms > 0"`
	ModelProfileID      string `gorm:"column:model_profile_id;not null;default:''"`
	CreatedAt           int64  `gorm:"column:created_at;not null"`
	UpdatedAt           int64  `gorm:"column:updated_at;not null"`
	Revision            uint64 `gorm:"column:revision;not null;check:chk_cron_revision,revision > 0"`
	NextRunAt           int64  `gorm:"column:next_run_at;not null"`
	RetryAt             *int64 `gorm:"column:retry_at"`
	CircuitOpenUntil    *int64 `gorm:"column:circuit_open_until"`
	ConsecutiveFailures int64  `gorm:"column:consecutive_failures;not null;default:0"`
	LastRunAt           *int64 `gorm:"column:last_run_at"`
	LastScheduledFor    *int64 `gorm:"column:last_scheduled_for"`
	LastCoalesced       bool   `gorm:"column:last_coalesced;not null;default:0;check:chk_cron_last_coalesced,last_coalesced IN (false,true)"`
	LastError           string `gorm:"column:last_error;not null;default:''"`
	LeaseOwner          string `gorm:"column:lease_owner;not null;default:''"`
	LeaseExpiresAt      *int64 `gorm:"column:lease_expires_at"`
	RunID               string `gorm:"column:run_id;not null;default:''"`
	RunScheduledFor     *int64 `gorm:"column:run_scheduled_for"`
	RunStartedAt        *int64 `gorm:"column:run_started_at"`
	RunDeadline         *int64 `gorm:"column:run_deadline"`
	RunCoalesced        bool   `gorm:"column:run_coalesced;not null;default:0;check:chk_cron_run_coalesced,run_coalesced IN (false,true)"`
	OwnerID             string `gorm:"column:owner_id;not null;default:'';index:idx_cron_job_owner"`
}

func (v103Cron) TableName() string { return "cron_job" }

type v103KnownHost struct {
	ID          string `gorm:"column:id;primaryKey"`
	Host        string `gorm:"column:host;not null;uniqueIndex:known_host_owner_endpoint_unique,priority:2"`
	Port        int32  `gorm:"column:port;not null;uniqueIndex:known_host_owner_endpoint_unique,priority:3"`
	KeyType     string `gorm:"column:key_type;not null;uniqueIndex:known_host_owner_endpoint_unique,priority:4"`
	Fingerprint string `gorm:"column:fingerprint;not null"`
	AddedAt     int64  `gorm:"column:added_at;not null"`
	OwnerID     string `gorm:"column:owner_id;not null;default:'';index:idx_known_host_owner;uniqueIndex:known_host_owner_endpoint_unique,priority:1"`
}

func (v103KnownHost) TableName() string { return "known_host" }

type v103SyncTombstone struct {
	ID        string `gorm:"column:id;primaryKey"`
	Kind      string `gorm:"column:kind;not null"`
	DeletedAt int64  `gorm:"column:deleted_at;not null"`
	OwnerID   string `gorm:"column:owner_id;primaryKey;default:'';index:idx_sync_tombstone_owner"`
}

func (v103SyncTombstone) TableName() string { return "sync_tombstone" }

type v103CredentialTombstone struct {
	ID        string `gorm:"column:id;primaryKey"`
	DeletedAt int64  `gorm:"column:deleted_at;not null"`
	OwnerID   string `gorm:"column:owner_id;primaryKey;default:'';index:idx_credential_tombstone_owner"`
}

func (v103CredentialTombstone) TableName() string { return "credential_tombstone" }

type v103UserDEK struct {
	UserID           string `gorm:"column:user_id;primaryKey"`
	DEKEnvelope      []byte `gorm:"column:dek_envelope;not null"`
	KDFSalt          []byte `gorm:"column:kdf_salt;not null"`
	KDFParams        string `gorm:"column:kdf_params;not null"`
	RecoveryEnvelope []byte `gorm:"column:recovery_envelope;not null"`
	RecoveryHash     string `gorm:"column:recovery_hash;not null"`
	CreatedAt        int64  `gorm:"column:created_at;not null"`
	UpdatedAt        int64  `gorm:"column:updated_at;not null"`
}

func (v103UserDEK) TableName() string { return "user_dek" }

type v103UserSyncObject struct {
	UserID   string `gorm:"column:user_id;primaryKey;index:idx_user_sync_object_seq,priority:1"`
	ID       string `gorm:"column:id;primaryKey"`
	Seq      int64  `gorm:"column:seq;not null;index:idx_user_sync_object_seq,priority:2"`
	Blob     []byte `gorm:"column:blob;not null"`
	BlobHash []byte `gorm:"column:blob_hash"`
}

func (v103UserSyncObject) TableName() string { return "user_sync_object" }

type v103UserSyncHead struct {
	UserID   string `gorm:"column:user_id;primaryKey"`
	HeadHash string `gorm:"column:head_hash;not null"`
}

func (v103UserSyncHead) TableName() string { return "user_sync_head" }

type v103SyncState struct {
	UserID      string `gorm:"column:user_id;primaryKey"`
	ObjectID    string `gorm:"column:object_id;primaryKey"`
	PayloadHash []byte `gorm:"column:payload_hash"`
	BlobHash    []byte `gorm:"column:blob_hash;not null"`
}

func (v103SyncState) TableName() string { return "sync_state" }

type v103UserTOTP struct {
	UserID                string `gorm:"column:user_id;primaryKey"`
	SecretEnvelope        []byte `gorm:"column:secret_envelope"`
	PendingSecretEnvelope []byte `gorm:"column:pending_secret_envelope"`
	CreatedAt             int64  `gorm:"column:created_at;not null"`
	UpdatedAt             int64  `gorm:"column:updated_at;not null"`
}

func (v103UserTOTP) TableName() string { return "user_totp" }

type v111Cron struct {
	ID                  string `gorm:"column:id;primaryKey"`
	SessionID           string `gorm:"column:session_id;not null;index:idx_cron_job_session"`
	Name                string `gorm:"column:name;not null;default:''"`
	Prompt              string `gorm:"column:prompt;not null"`
	Schedule            string `gorm:"column:schedule;not null"`
	Timezone            string `gorm:"column:timezone;not null"`
	Enabled             bool   `gorm:"column:enabled;not null;check:chk_cron_enabled,enabled IN (false,true)"`
	TimeoutMS           int64  `gorm:"column:timeout_ms;not null;check:chk_cron_timeout,timeout_ms > 0"`
	ModelProfileID      string `gorm:"column:model_profile_id;not null;default:''"`
	CreatedAt           int64  `gorm:"column:created_at;not null"`
	UpdatedAt           int64  `gorm:"column:updated_at;not null"`
	Revision            uint64 `gorm:"column:revision;not null;check:chk_cron_revision,revision > 0"`
	NextRunAt           int64  `gorm:"column:next_run_at;not null"`
	RetryAt             *int64 `gorm:"column:retry_at"`
	CircuitOpenUntil    *int64 `gorm:"column:circuit_open_until"`
	ConsecutiveFailures int64  `gorm:"column:consecutive_failures;not null;default:0"`
	LastRunAt           *int64 `gorm:"column:last_run_at"`
	LastScheduledFor    *int64 `gorm:"column:last_scheduled_for"`
	LastCoalesced       bool   `gorm:"column:last_coalesced;not null;default:0;check:chk_cron_last_coalesced,last_coalesced IN (false,true)"`
	LastError           string `gorm:"column:last_error;not null;default:''"`
	LeaseOwner          string `gorm:"column:lease_owner;not null;default:''"`
	LeaseExpiresAt      *int64 `gorm:"column:lease_expires_at"`
	RunID               string `gorm:"size:191;column:run_id;not null;default:''"`
	RunScheduledFor     *int64 `gorm:"column:run_scheduled_for"`
	RunStartedAt        *int64 `gorm:"column:run_started_at"`
	RunDeadline         *int64 `gorm:"column:run_deadline"`
	RunCoalesced        bool   `gorm:"column:run_coalesced;not null;default:0;check:chk_cron_run_coalesced,run_coalesced IN (false,true)"`
	OwnerID             string `gorm:"size:191;column:owner_id;not null;default:'';index:idx_cron_job_owner"`
}

func (v111Cron) TableName() string { return "cron_job" }

type v111KnownHost struct {
	ID          string `gorm:"column:id;primaryKey"`
	Host        string `gorm:"column:host;not null;size:191;uniqueIndex:known_host_owner_endpoint_unique,priority:2"`
	Port        int32  `gorm:"column:port;not null;uniqueIndex:known_host_owner_endpoint_unique,priority:3"`
	KeyType     string `gorm:"column:key_type;not null;size:191;uniqueIndex:known_host_owner_endpoint_unique,priority:4"`
	Fingerprint string `gorm:"column:fingerprint;not null"`
	AddedAt     int64  `gorm:"column:added_at;not null"`
	OwnerID     string `gorm:"size:191;column:owner_id;not null;default:'';index:idx_known_host_owner;uniqueIndex:known_host_owner_endpoint_unique,priority:1"`
}

func (v111KnownHost) TableName() string { return "known_host" }
