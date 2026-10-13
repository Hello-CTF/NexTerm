package model

type TranscriptRow struct {
	ID             string `gorm:"column:id;primaryKey" json:"id"`
	SessionID      string `gorm:"column:session_id;not null;index:idx_transcript_session" json:"sessionId"`
	AssetID        string `gorm:"column:asset_id;not null;index:idx_transcript_asset_started,priority:1" json:"assetId"`
	AssetName      string `gorm:"column:asset_name;not null;default:''" json:"assetName"`
	AssetKind      string `gorm:"column:asset_kind;not null;default:''" json:"assetKind"`
	StartedAt      int64  `gorm:"column:started_at;not null;index:idx_transcript_asset_started,priority:2,sort:desc" json:"startedAt"`
	EndedAt        *int64 `gorm:"column:ended_at" json:"endedAt"`
	Bytes          int64  `gorm:"column:bytes;not null;default:0" json:"bytes"`
	Chunks         int64  `gorm:"column:chunks;not null;default:0" json:"chunks"`
	Truncated      bool   `gorm:"column:truncated;not null;default:0" json:"truncated"`
	SyncOptIn      bool   `gorm:"column:sync_opt_in;not null;default:0" json:"syncOptIn"`
	ContentOmitted bool   `gorm:"column:content_omitted;not null;default:0" json:"contentOmitted"`
	OwnerID        string `gorm:"column:owner_id;not null;default:'';index:idx_transcript_owner" json:"owner_id"`
}

func (TranscriptRow) TableName() string { return "transcript" }

type TranscriptChunkRow struct {
	TranscriptID string        `gorm:"column:transcript_id;primaryKey" json:"transcriptId"`
	Seq          int64         `gorm:"column:seq;primaryKey" json:"seq"`
	TabID        string        `gorm:"column:tab_id;not null;default:''" json:"tabId"`
	TS           int64         `gorm:"column:ts;not null" json:"ts"`
	Data         []byte        `gorm:"column:data;not null" json:"data"`
	Kind         int           `gorm:"column:kind;not null;default:0" json:"kind"`
	OwnerID      string        `gorm:"column:owner_id;not null;default:'';index:idx_transcript_chunk_owner" json:"owner_id"`
	Transcript   TranscriptRow `gorm:"foreignKey:TranscriptID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (TranscriptChunkRow) TableName() string { return "transcript_chunk" }

type DurableTranscriptOffset struct {
	DurableID string `gorm:"column:durable_id;primaryKey" json:"durableId"`
	Offset    int64  `gorm:"column:offset;not null" json:"offset"`
	UpdatedAt int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
	OwnerID   string `gorm:"column:owner_id;not null;default:'';index:idx_durable_transcript_offset_owner" json:"owner_id"`
}

func (DurableTranscriptOffset) TableName() string { return "durable_transcript_offset" }

type DurableTabTitle struct {
	DurableID string `gorm:"column:durable_id;primaryKey" json:"durableId"`
	Title     string `gorm:"column:title;not null" json:"title"`
	UpdatedAt int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
	OwnerID   string `gorm:"column:owner_id;not null;default:'';index:idx_durable_tab_title_owner" json:"owner_id"`
}

func (DurableTabTitle) TableName() string { return "durable_tab_title" }

type CredentialTombstone struct {
	ID        string `gorm:"column:id;primaryKey" json:"id"`
	DeletedAt int64  `gorm:"column:deleted_at;not null" json:"deletedAt"`
	OwnerID   string `gorm:"column:owner_id;primaryKey;default:'';index:idx_credential_tombstone_owner" json:"owner_id"`
}

func (CredentialTombstone) TableName() string { return "credential_tombstone" }

type SyncTombstone struct {
	ID        string `gorm:"column:id;primaryKey" json:"id"`
	Kind      string `gorm:"column:kind;not null" json:"kind"`
	DeletedAt int64  `gorm:"column:deleted_at;not null" json:"deletedAt"`
	OwnerID   string `gorm:"column:owner_id;primaryKey;default:'';index:idx_sync_tombstone_owner" json:"owner_id"`
}

func (SyncTombstone) TableName() string { return "sync_tombstone" }

type SyncState struct {
	UserID      string `gorm:"column:user_id;primaryKey" json:"userId"`
	ObjectID    string `gorm:"column:object_id;primaryKey" json:"objectId"`
	PayloadHash []byte `gorm:"column:payload_hash" json:"payloadHash"`
	BlobHash    []byte `gorm:"column:blob_hash;not null" json:"blobHash"`
}

func (SyncState) TableName() string { return "sync_state" }

type MemoryEntry struct {
	ID        string `gorm:"column:id;primaryKey" json:"id"`
	Tenant    string `gorm:"column:tenant;not null" json:"tenant"`
	Subject   string `gorm:"column:subject;not null" json:"subject"`
	Topic     string `gorm:"column:topic;not null" json:"topic"`
	Content   string `gorm:"column:content;not null" json:"content"`
	Version   int64  `gorm:"column:version;not null" json:"version"`
	Redacted  bool   `gorm:"column:redacted;not null" json:"redacted"`
	CreatedAt int64  `gorm:"column:created_at;not null" json:"createdAt"`
	UpdatedAt int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
}

func (MemoryEntry) TableName() string { return "memory_entry" }

type MemorySettings struct {
	Tenant           string `gorm:"column:tenant;primaryKey" json:"tenant"`
	Subject          string `gorm:"column:subject;primaryKey" json:"subject"`
	InjectionEnabled bool   `gorm:"column:injection_enabled;not null" json:"injectionEnabled"`
	ToolsEnabled     bool   `gorm:"column:tools_enabled;not null;default:0" json:"toolsEnabled"`
	Version          int64  `gorm:"column:version;not null" json:"version"`
	UpdatedAt        int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
}

func (MemorySettings) TableName() string { return "memory_settings" }
