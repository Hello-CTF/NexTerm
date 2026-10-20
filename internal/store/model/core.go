package model

type AssetGroupRow struct {
	ID        string         `gorm:"column:id;primaryKey" json:"id"`
	ParentID  *string        `gorm:"column:parent_id;size:191" json:"parent_id"`
	Name      string         `gorm:"column:name;not null" json:"name"`
	Sort      int64          `gorm:"column:sort;not null" json:"sort"`
	CreatedAt int64          `gorm:"column:created_at;not null" json:"created_at"`
	UpdatedAt int64          `gorm:"column:updated_at;not null" json:"updated_at"`
	OwnerID   string         `gorm:"size:191;column:owner_id;not null;default:'';index:idx_asset_group_owner" json:"owner_id"`
	Parent    *AssetGroupRow `gorm:"foreignKey:ParentID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (AssetGroupRow) TableName() string { return "asset_group" }

type AssetRow struct {
	ID          string         `gorm:"column:id;primaryKey" json:"id"`
	GroupID     *string        `gorm:"size:191;column:group_id;index:idx_asset_group" json:"groupId"`
	Kind        string         `gorm:"column:kind;not null;index:idx_asset_kind" json:"kind"`
	Name        string         `gorm:"column:name;not null" json:"name"`
	Host        *string        `gorm:"column:host" json:"host"`
	Port        *int32         `gorm:"column:port" json:"port"`
	Username    *string        `gorm:"column:username" json:"username"`
	AuthKind    *string        `gorm:"column:auth_kind" json:"authKind"`
	KeyPath     *string        `gorm:"column:key_path" json:"keyPath"`
	CredID      *string        `gorm:"column:cred_id;size:191" json:"credId"`
	OptionsJSON string         `gorm:"column:options_json;not null;default:'{}'" json:"optionsJson"`
	Tags        string         `gorm:"column:tags;not null;default:''" json:"tags"`
	Note        string         `gorm:"column:note;not null;default:''" json:"note"`
	Sort        int64          `gorm:"column:sort;not null" json:"sort"`
	CreatedAt   int64          `gorm:"column:created_at;not null" json:"createdAt"`
	UpdatedAt   int64          `gorm:"column:updated_at;not null" json:"updatedAt"`
	DeletedAt   *int64         `gorm:"column:deleted_at" json:"deletedAt"`
	Builtin     bool           `gorm:"column:builtin;not null;default:0" json:"builtin"`
	OwnerID     string         `gorm:"size:191;column:owner_id;not null;default:'';index:idx_asset_owner" json:"owner_id"`
	Group       *AssetGroupRow `gorm:"foreignKey:GroupID;references:ID;constraint:OnDelete:SET NULL" json:"-"`
	Credential  *CredentialRow `gorm:"foreignKey:CredID;references:ID;constraint:OnDelete:SET NULL" json:"-"`
}

func (AssetRow) TableName() string { return "asset" }

type CredentialRow struct {
	ID        string `gorm:"column:id;primaryKey" json:"id"`
	Name      string `gorm:"column:name;not null" json:"name"`
	Kind      string `gorm:"column:kind;not null" json:"kind"`
	Cipher    string `gorm:"column:cipher;not null" json:"cipher"`
	Nonce     []byte `gorm:"column:nonce;not null" json:"-"`
	Blob      []byte `gorm:"column:blob;not null" json:"-"`
	KEKHint   string `gorm:"column:kek_hint;not null" json:"kekHint"`
	CreatedAt int64  `gorm:"column:created_at;not null" json:"createdAt"`
	UpdatedAt int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
	OwnerID   string `gorm:"size:191;column:owner_id;not null;default:'';index:idx_credential_owner" json:"owner_id"`
}

func (CredentialRow) TableName() string { return "credential" }

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

type Setting struct {
	Key       string `gorm:"column:key;primaryKey" json:"key"`
	Value     string `gorm:"column:value;not null" json:"value"`
	UpdatedAt int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
}

func (Setting) TableName() string { return "setting" }

type AuditRow struct {
	ID          int64   `gorm:"column:id;primaryKey;autoIncrement;index:idx_audit_retention,priority:2,sort:desc" json:"id"`
	TS          int64   `gorm:"column:ts;not null;index:idx_audit_retention,priority:1,sort:desc" json:"ts"`
	SessionID   *string `gorm:"column:session_id" json:"sessionId"`
	AssetID     *string `gorm:"column:asset_id" json:"assetId"`
	Source      string  `gorm:"column:source;not null" json:"source"`
	Kind        string  `gorm:"column:kind;not null" json:"kind"`
	PayloadJSON string  `gorm:"column:payload_json;not null" json:"payloadJson"`
	ExitCode    *int32  `gorm:"column:exit_code" json:"exitCode"`
	DurationMS  *int64  `gorm:"column:duration_ms" json:"durationMs"`
	OwnerID     string  `gorm:"size:191;column:owner_id;not null;default:'';index:idx_audit_log_owner" json:"owner_id"`
}

func (AuditRow) TableName() string { return "audit_log" }

type ConversationRow struct {
	ID        string `gorm:"column:id;primaryKey" json:"id"`
	Title     string `gorm:"column:title;not null;default:''" json:"title"`
	ScopeJSON string `gorm:"column:scope_json;not null;default:'{}'" json:"scopeJson"`
	CreatedAt int64  `gorm:"column:created_at;not null" json:"createdAt"`
	UpdatedAt int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
	OwnerID   string `gorm:"size:191;column:owner_id;not null;default:'';index:idx_ai_conversation_owner" json:"owner_id"`
}

func (ConversationRow) TableName() string { return "ai_conversation" }

type MessageRow struct {
	ID             string          `gorm:"column:id;primaryKey" json:"id"`
	ConversationID string          `gorm:"column:conversation_id;not null;size:191;uniqueIndex:idx_ai_msg_conv_seq,priority:1" json:"conversationId"`
	Role           string          `gorm:"column:role;not null" json:"role"`
	ContentJSON    string          `gorm:"column:content_json;not null" json:"contentJson"`
	TokensIn       *int64          `gorm:"column:tokens_in" json:"tokensIn"`
	TokensOut      *int64          `gorm:"column:tokens_out" json:"tokensOut"`
	CreatedAt      int64           `gorm:"column:created_at;not null" json:"createdAt"`
	Seq            int64           `gorm:"column:seq;not null;default:0;uniqueIndex:idx_ai_msg_conv_seq,priority:2" json:"seq"`
	OwnerID        string          `gorm:"size:191;column:owner_id;not null;default:'';index:idx_ai_message_owner" json:"owner_id"`
	Conversation   ConversationRow `gorm:"foreignKey:ConversationID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (MessageRow) TableName() string { return "ai_message" }

type SnippetRow struct {
	ID        string  `gorm:"column:id;primaryKey" json:"id"`
	GroupID   *string `gorm:"size:191;column:group_id" json:"group_id"`
	Name      string  `gorm:"column:name;not null" json:"name"`
	Body      string  `gorm:"column:body;not null" json:"body"`
	Sort      int64   `gorm:"column:sort;not null" json:"sort"`
	CreatedAt int64   `gorm:"column:created_at;not null" json:"created_at"`
	UpdatedAt int64   `gorm:"column:updated_at;not null" json:"updated_at"`
	OwnerID   string  `gorm:"size:191;column:owner_id;not null;default:'';index:idx_snippet_owner" json:"owner_id"`
}

func (SnippetRow) TableName() string { return "snippet" }

type KnownHostRow struct {
	ID          string `gorm:"column:id;primaryKey" json:"id"`
	Host        string `gorm:"column:host;not null;size:191;uniqueIndex:known_host_owner_endpoint_unique,priority:2" json:"host"`
	Port        int32  `gorm:"column:port;not null;uniqueIndex:known_host_owner_endpoint_unique,priority:3" json:"port"`
	KeyType     string `gorm:"column:key_type;not null;size:191;uniqueIndex:known_host_owner_endpoint_unique,priority:4" json:"keyType"`
	Fingerprint string `gorm:"column:fingerprint;not null" json:"fingerprint"`
	Key         string `gorm:"column:key;not null;default:''" json:"key"`
	// OpToken is the unique identity of the accept operation that last wrote the row; it backs
	// conditional compensation, which wall-clock added_at cannot disambiguate. It is not synced state.
	OpToken string `gorm:"column:op_token;not null;default:''" json:"-"`
	AddedAt int64  `gorm:"column:added_at;not null" json:"addedAt"`
	OwnerID string `gorm:"size:191;column:owner_id;not null;default:'';index:idx_known_host_owner;uniqueIndex:known_host_owner_endpoint_unique,priority:1" json:"owner_id"`
}

func (KnownHostRow) TableName() string { return "known_host" }

type RecordingRow struct {
	ID        string `gorm:"column:id;primaryKey;index:idx_terminal_recording_retention,priority:2,sort:desc" json:"id"`
	SessionID string `gorm:"column:session_id;not null" json:"session_id"`
	TabID     string `gorm:"column:tab_id;not null" json:"tab_id"`
	Path      string `gorm:"column:path;not null" json:"path"`
	Bytes     int64  `gorm:"column:bytes;not null;default:0" json:"bytes"`
	StartedAt int64  `gorm:"column:started_at;not null" json:"startedAt"`
	EndedAt   *int64 `gorm:"column:ended_at;index:idx_terminal_recording_retention,priority:1,sort:desc,where:ended_at IS NOT NULL" json:"endedAt"`
	OwnerID   string `gorm:"size:191;column:owner_id;not null;default:'';index:idx_terminal_recording_owner" json:"owner_id"`
}

func (RecordingRow) TableName() string { return "terminal_recording" }

type OutcomeRecord struct {
	IdempotenceKey   string `gorm:"column:idempotence_key;primaryKey" json:"idempotenceKey"`
	AuthorizationID  string `gorm:"column:authorization_id;not null" json:"authorizationId"`
	Kind             string `gorm:"column:kind;not null" json:"kind"`
	CanonicalArgs    string `gorm:"column:canonical_arguments;not null" json:"canonicalArguments"`
	State            string `gorm:"column:state;not null" json:"state"`
	Outcome          string `gorm:"column:outcome;not null" json:"outcome"`
	ExitCode         *int32 `gorm:"column:exit_code" json:"exitCode"`
	ResultError      string `gorm:"column:result_error;not null;default:''" json:"resultError"`
	AuditState       string `gorm:"column:audit_state;not null" json:"auditState"`
	AuditError       string `gorm:"column:audit_error;not null;default:''" json:"auditError"`
	AuditCompletedAt *int64 `gorm:"column:audit_completed_at" json:"auditCompletedAt"`
	Revision         uint64 `gorm:"column:revision;not null" json:"revision"`
	CreatedAt        int64  `gorm:"column:created_at;not null" json:"createdAt"`
	StartedAt        *int64 `gorm:"column:started_at" json:"startedAt"`
	FinishedAt       *int64 `gorm:"column:finished_at" json:"finishedAt"`
	OwnerID          string `gorm:"size:191;column:owner_id;primaryKey;default:'';index:idx_outcome_record_owner" json:"owner_id"`
}

func (OutcomeRecord) TableName() string { return "outcome_record" }

type CronJob struct {
	ID                  string `gorm:"column:id;primaryKey" json:"id"`
	SessionID           string `gorm:"column:session_id;not null;index:idx_cron_job_session" json:"sessionId"`
	Name                string `gorm:"column:name;not null;default:''" json:"name"`
	Prompt              string `gorm:"column:prompt;not null" json:"prompt"`
	Schedule            string `gorm:"column:schedule;not null" json:"schedule"`
	Timezone            string `gorm:"column:timezone;not null" json:"timezone"`
	Enabled             bool   `gorm:"column:enabled;not null;check:chk_cron_enabled,enabled IN (false,true);index:idx_cron_job_due,priority:1" json:"enabled"`
	TimeoutMS           int64  `gorm:"column:timeout_ms;not null;check:chk_cron_timeout,timeout_ms > 0" json:"timeoutMs"`
	ModelProfileID      string `gorm:"column:model_profile_id;not null;default:''" json:"modelProfileId"`
	CreatedAt           int64  `gorm:"column:created_at;not null" json:"createdAt"`
	UpdatedAt           int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
	Revision            uint64 `gorm:"column:revision;not null;check:chk_cron_revision,revision > 0" json:"revision"`
	NextRunAt           int64  `gorm:"column:next_run_at;not null;index:idx_cron_job_due,priority:2" json:"nextRunAt"`
	RetryAt             *int64 `gorm:"column:retry_at" json:"retryAt"`
	CircuitOpenUntil    *int64 `gorm:"column:circuit_open_until" json:"circuitOpenUntil"`
	ConsecutiveFailures int64  `gorm:"column:consecutive_failures;not null;default:0" json:"consecutiveFailures"`
	LastRunAt           *int64 `gorm:"column:last_run_at" json:"lastRunAt"`
	LastScheduledFor    *int64 `gorm:"column:last_scheduled_for" json:"lastScheduledFor"`
	LastCoalesced       bool   `gorm:"column:last_coalesced;not null;default:0;check:chk_cron_last_coalesced,last_coalesced IN (false,true)" json:"lastCoalesced"`
	LastError           string `gorm:"column:last_error;not null;default:''" json:"lastError"`
	LeaseOwner          string `gorm:"column:lease_owner;not null;default:''" json:"leaseOwner"`
	LeaseExpiresAt      *int64 `gorm:"column:lease_expires_at" json:"leaseExpiresAt"`
	RunID               string `gorm:"size:191;column:run_id;not null;default:'';index:idx_cron_job_recovery,priority:1" json:"runId"`
	RunScheduledFor     *int64 `gorm:"column:run_scheduled_for" json:"runScheduledFor"`
	RunStartedAt        *int64 `gorm:"column:run_started_at" json:"runStartedAt"`
	RunDeadline         *int64 `gorm:"column:run_deadline;index:idx_cron_job_recovery,priority:2" json:"runDeadline"`
	RunCoalesced        bool   `gorm:"column:run_coalesced;not null;default:0;check:chk_cron_run_coalesced,run_coalesced IN (false,true)" json:"runCoalesced"`
	OwnerID             string `gorm:"size:191;column:owner_id;not null;default:'';index:idx_cron_job_owner" json:"owner_id"`
	OwnerKind           string `gorm:"size:16;column:owner_kind;not null;default:''" json:"owner_kind"`
}

func (CronJob) TableName() string { return "cron_job" }

type RunRow struct {
	ID                  string          `gorm:"column:id;primaryKey" json:"id"`
	ConversationID      string          `gorm:"column:conversation_id;not null;size:191;index:idx_ai_run_conv,priority:1" json:"conversationId"`
	Status              string          `gorm:"column:status;not null;index:idx_ai_run_status" json:"status"`
	Attempt             int64           `gorm:"column:attempt;not null;default:1" json:"attempt"`
	Seq                 uint64          `gorm:"column:seq;not null;default:0" json:"seq"`
	PlanMode            bool            `gorm:"column:plan_mode;not null;default:0" json:"planMode"`
	Source              string          `gorm:"column:source;not null;default:'chat'" json:"source"`
	ProfileID           string          `gorm:"column:profile_id;not null;default:'';index:idx_ai_run_profile,priority:1" json:"profileId"`
	Answer              string          `gorm:"column:answer;not null;default:''" json:"answer"`
	Turns               int             `gorm:"column:turns;not null;default:0" json:"turns"`
	TokensIn            int64           `gorm:"column:tokens_in;not null;default:0" json:"tokensIn"`
	TokensOut           int64           `gorm:"column:tokens_out;not null;default:0" json:"tokensOut"`
	CacheCreationTokens int64           `gorm:"column:cache_creation_tokens;not null;default:0" json:"cacheCreationTokens"`
	LatencyMS           int64           `gorm:"column:latency_ms;not null;default:0" json:"latencyMs"`
	Error               string          `gorm:"column:error;not null;default:''" json:"error"`
	CreatedAt           int64           `gorm:"column:created_at;not null;index:idx_ai_run_conv,priority:2;index:idx_ai_run_profile,priority:2" json:"createdAt"`
	UpdatedAt           int64           `gorm:"column:updated_at;not null" json:"updatedAt"`
	FinishedAt          *int64          `gorm:"column:finished_at" json:"finishedAt"`
	OwnerID             string          `gorm:"size:191;column:owner_id;not null;default:'';index:idx_ai_run_owner" json:"owner_id"`
	Conversation        ConversationRow `gorm:"foreignKey:ConversationID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (RunRow) TableName() string { return "ai_run" }

type RunEventRow struct {
	RunID       string `gorm:"column:run_id;primaryKey;size:191" json:"runId"`
	Seq         uint64 `gorm:"column:seq;primaryKey" json:"seq"`
	Type        string `gorm:"column:type;not null" json:"type"`
	PayloadJSON string `gorm:"column:payload_json;not null" json:"payloadJson"`
	CreatedAt   int64  `gorm:"column:created_at;not null" json:"createdAt"`
	OwnerID     string `gorm:"size:191;column:owner_id;not null;default:'';index:idx_ai_run_event_owner" json:"owner_id"`
	Run         RunRow `gorm:"foreignKey:RunID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (RunEventRow) TableName() string { return "ai_run_event" }

type HitlRunRow struct {
	ID        string `gorm:"column:id;primaryKey" json:"id"`
	Data      []byte `gorm:"column:data;not null" json:"data"`
	UpdatedAt int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
	OwnerID   string `gorm:"size:191;column:owner_id;not null;default:'';index:idx_ai_hitl_run_owner" json:"owner_id"`
}

func (HitlRunRow) TableName() string { return "ai_hitl_run" }

type CheckpointRow struct {
	ID        string `gorm:"column:id;primaryKey" json:"id"`
	Data      []byte `gorm:"column:data;not null" json:"data"`
	UpdatedAt int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
	OwnerID   string `gorm:"size:191;column:owner_id;not null;default:'';index:idx_ai_checkpoint_owner" json:"owner_id"`
}

func (CheckpointRow) TableName() string { return "ai_checkpoint" }
