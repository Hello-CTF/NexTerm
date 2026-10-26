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
	StoredBytes    int64  `gorm:"column:stored_bytes;not null;default:0" json:"storedBytes"`
	Chunks         int64  `gorm:"column:chunks;not null;default:0" json:"chunks"`
	Truncated      bool   `gorm:"column:truncated;not null;default:0" json:"truncated"`
	SyncOptIn      bool   `gorm:"column:sync_opt_in;not null;default:0" json:"syncOptIn"`
	ContentOmitted bool   `gorm:"column:content_omitted;not null;default:0" json:"contentOmitted"`
	// ContentGen is the transcript's content generation: it bumps on every
	// whole-content replacement (sync apply, replace) so timestamp-index
	// rows written for an older content generation are recognizable as
	// stale and never trusted for the current content. Appends do not bump
	// it: existing blocks are unchanged by an append.
	ContentGen int64  `gorm:"column:content_gen;not null;default:0" json:"contentGen"`
	OwnerID    string `gorm:"size:191;column:owner_id;not null;default:'';index:idx_transcript_owner" json:"owner_id"`
}

func (TranscriptRow) TableName() string { return "transcript" }

type TranscriptChunkRow struct {
	TranscriptID string        `gorm:"column:transcript_id;primaryKey" json:"transcriptId"`
	Seq          int64         `gorm:"column:seq;primaryKey" json:"seq"`
	TabID        string        `gorm:"column:tab_id;not null;default:''" json:"tabId"`
	TS           int64         `gorm:"column:ts;not null" json:"ts"`
	Data         []byte        `gorm:"column:data;not null" json:"data"`
	Kind         int           `gorm:"column:kind;not null;default:0" json:"kind"`
	OwnerID      string        `gorm:"size:191;column:owner_id;not null;default:'';index:idx_transcript_chunk_owner" json:"owner_id"`
	Transcript   TranscriptRow `gorm:"foreignKey:TranscriptID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (TranscriptChunkRow) TableName() string { return "transcript_chunk" }

// TranscriptChunkTS is the per-block timestamp index of a transcript's
// stored chunks: one row per stored block carrying the block's true
// maximum chunk timestamp. The distant seek prunes by this index — a block
// whose maximum timestamp is below the target provably holds no qualifying
// chunk under any timestamp order — so seeks stay bounded and correct for
// both ordered and out-of-order data, without narrowing what may be
// written. Rows are maintained by every chunk write path and backfilled
// for pre-index transcripts in resumable batches.
type TranscriptChunkTS struct {
	TranscriptID string `gorm:"column:transcript_id;primaryKey;index:idx_transcript_chunk_ts_max,priority:1" json:"transcriptId"`
	BlockSeq     int64  `gorm:"column:block_seq;primaryKey;index:idx_transcript_chunk_ts_max,priority:3" json:"blockSeq"`
	MaxTS        int64  `gorm:"column:max_ts;not null;index:idx_transcript_chunk_ts_max,priority:2" json:"maxTs"`
	// Gen is the content generation of the transcript the row was computed
	// from: rows stamped with an older generation are stale (a replace
	// rebuilt the content and the index) and every index read filters them
	// out, so a backfill insert racing a replace can never be mistaken for
	// the current index.
	Gen        int64         `gorm:"column:gen;not null;default:0" json:"gen"`
	OwnerID    string        `gorm:"size:191;column:owner_id;not null;default:'';index:idx_transcript_chunk_ts_owner" json:"owner_id"`
	Transcript TranscriptRow `gorm:"foreignKey:TranscriptID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (TranscriptChunkTS) TableName() string { return "transcript_chunk_ts" }

// TranscriptTSIndex tracks a transcript's timestamp-index completeness.
// Complete reports every stored block has an index row, so the seek can
// trust the index alone; until then the seek eliminates candidates from
// the indexed prefix and scans the remaining suffix soundly while the
// backfill advances in bounded batches. VerifiedThrough is the seek-side
// verification watermark: every block with seq <= VerifiedThrough has had
// its decoded content verified against its index row for the current
// content, so a seek whose candidate lies beyond it only verifies the
// newly reached blocks — bounded and resumable across calls, never an
// unbounded prefix scan. Revision is the watermark's rollback epoch: it
// increments whenever the watermark moves backward (an early-seq append
// rolls it back, a whole-content replacement resets it), so a seek's
// advance can compare against the epoch it read in its snapshot and a
// stale advance is discarded instead of re-covering blocks the snapshot
// never verified.
type TranscriptTSIndex struct {
	TranscriptID    string        `gorm:"column:transcript_id;primaryKey" json:"transcriptId"`
	VerifiedThrough int64         `gorm:"column:verified_through;not null;default:0" json:"verifiedThrough"`
	Revision        int64         `gorm:"column:revision;not null;default:0" json:"revision"`
	Complete        bool          `gorm:"column:complete;not null;default:0" json:"complete"`
	UpdatedAt       int64         `gorm:"column:updated_at;not null" json:"updatedAt"`
	OwnerID         string        `gorm:"size:191;column:owner_id;not null;default:'';index:idx_transcript_ts_index_owner" json:"owner_id"`
	Transcript      TranscriptRow `gorm:"foreignKey:TranscriptID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (TranscriptTSIndex) TableName() string { return "transcript_ts_index" }

type DurableTranscriptOffset struct {
	DurableID string `gorm:"column:durable_id;primaryKey" json:"durableId"`
	Offset    int64  `gorm:"column:offset;not null" json:"offset"`
	UpdatedAt int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
	OwnerID   string `gorm:"size:191;column:owner_id;not null;default:'';index:idx_durable_transcript_offset_owner" json:"owner_id"`
}

func (DurableTranscriptOffset) TableName() string { return "durable_transcript_offset" }

type DurableTabTitle struct {
	DurableID string `gorm:"column:durable_id;primaryKey" json:"durableId"`
	Title     string `gorm:"column:title;not null" json:"title"`
	UpdatedAt int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
	OwnerID   string `gorm:"size:191;column:owner_id;not null;default:'';index:idx_durable_tab_title_owner" json:"owner_id"`
	// ChangedAt is the title decision time (Unix ms, wall clock) clients order
	// renames by; zero for rows written before it was recorded.
	ChangedAt int64 `gorm:"column:changed_at;not null;default:0" json:"changedAt"`
}

func (DurableTabTitle) TableName() string { return "durable_tab_title" }

type CredentialTombstone struct {
	ID        string `gorm:"column:id;primaryKey" json:"id"`
	DeletedAt int64  `gorm:"column:deleted_at;not null" json:"deletedAt"`
	OwnerID   string `gorm:"size:191;column:owner_id;primaryKey;default:'';index:idx_credential_tombstone_owner" json:"owner_id"`
}

func (CredentialTombstone) TableName() string { return "credential_tombstone" }

type SyncTombstone struct {
	ID        string `gorm:"column:id;primaryKey" json:"id"`
	Kind      string `gorm:"column:kind;not null" json:"kind"`
	DeletedAt int64  `gorm:"column:deleted_at;not null" json:"deletedAt"`
	OwnerID   string `gorm:"size:191;column:owner_id;primaryKey;default:'';index:idx_sync_tombstone_owner" json:"owner_id"`
}

func (SyncTombstone) TableName() string { return "sync_tombstone" }

type SyncState struct {
	UserID      string `gorm:"size:191;column:user_id;primaryKey" json:"userId"`
	ObjectID    string `gorm:"column:object_id;primaryKey" json:"objectId"`
	PayloadHash []byte `gorm:"column:payload_hash" json:"payloadHash"`
	BlobHash    []byte `gorm:"column:blob_hash;not null" json:"blobHash"`
}

func (SyncState) TableName() string { return "sync_state" }

type MemoryEntry struct {
	ID        string `gorm:"size:64;column:id;primaryKey" json:"id"`
	Tenant    string `gorm:"size:256;column:tenant;not null;index:memory_entry_scope_topic,priority:1" json:"tenant"`
	Subject   string `gorm:"size:256;column:subject;not null;index:memory_entry_scope_topic,priority:2" json:"subject"`
	Topic     string `gorm:"size:256;column:topic;not null;index:memory_entry_scope_topic,priority:3" json:"topic"`
	Content   string `gorm:"column:content;not null" json:"content"`
	Version   int64  `gorm:"column:version;not null" json:"version"`
	Redacted  bool   `gorm:"column:redacted;not null" json:"redacted"`
	CreatedAt int64  `gorm:"column:created_at;not null" json:"createdAt"`
	UpdatedAt int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
}

func (MemoryEntry) TableName() string { return "memory_entry" }

type MemorySettings struct {
	Tenant           string `gorm:"size:256;column:tenant;primaryKey" json:"tenant"`
	Subject          string `gorm:"size:256;column:subject;primaryKey" json:"subject"`
	InjectionEnabled bool   `gorm:"column:injection_enabled;not null" json:"injectionEnabled"`
	ToolsEnabled     bool   `gorm:"column:tools_enabled;not null;default:0" json:"toolsEnabled"`
	Version          int64  `gorm:"column:version;not null" json:"version"`
	UpdatedAt        int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
}

func (MemorySettings) TableName() string { return "memory_settings" }
