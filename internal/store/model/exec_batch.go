package model

type ExecBatchRow struct {
	OwnerID       string `gorm:"column:owner_id;size:191;primaryKey;default:'';index:idx_exec_batch_owner_started,priority:1" json:"-"`
	ID            string `gorm:"column:id;size:128;primaryKey" json:"id"`
	CommandHash   string `gorm:"column:command_hash;size:64;not null" json:"commandHash"`
	TargetSetHash string `gorm:"column:target_set_hash;size:64;not null" json:"targetSetHash"`
	Status        string `gorm:"column:status;size:32;not null" json:"status"`
	TargetCount   int32  `gorm:"column:target_count;not null" json:"targetCount"`
	Production    bool   `gorm:"column:production;not null;default:0" json:"production"`
	StartedAt     int64  `gorm:"column:started_at;not null;index:idx_exec_batch_owner_started,priority:2,sort:desc;index:idx_exec_batch_retention,priority:2,sort:desc" json:"startedAt"`
	FinishedAt    *int64 `gorm:"column:finished_at;index:idx_exec_batch_retention,priority:1,sort:desc,where:finished_at IS NOT NULL" json:"finishedAt"`
	RunbookID     string `gorm:"column:runbook_id;size:128;not null;default:'';index:idx_exec_batch_runbook_run,priority:1" json:"runbookId,omitempty"`
	RunID         string `gorm:"column:run_id;size:128;not null;default:'';index:idx_exec_batch_runbook_run,priority:2" json:"runId,omitempty"`
	StepNumber    int32  `gorm:"column:step_number;not null;default:0" json:"stepNumber,omitempty"`
	StepCount     int32  `gorm:"column:step_count;not null;default:0" json:"stepCount,omitempty"`
	GroupNumber   int32  `gorm:"column:group_number;not null;default:0" json:"groupNumber,omitempty"`
	GroupCount    int32  `gorm:"column:group_count;not null;default:0" json:"groupCount,omitempty"`
	SyncOptIn     bool   `gorm:"column:sync_opt_in;not null;default:0" json:"syncOptIn"`
	// RunbookVersion is the persisted template revision a run started from; 0 for ad-hoc runbooks.
	RunbookVersion int32 `gorm:"column:runbook_version;not null;default:0" json:"runbookVersion,omitempty"`
}

func (ExecBatchRow) TableName() string { return "exec_batch" }

// RunbookTemplateRow is a persisted, per-owner runbook definition. Steps holds
// the JSON-encoded step list with command text snapshotted at save time (the
// originating snippet id is kept for provenance only). Version increments on
// every content update so run history can name the exact template revision.
type RunbookTemplateRow struct {
	OwnerID     string `gorm:"column:owner_id;size:191;primaryKey;default:'';index:idx_runbook_template_owner_name,priority:1" json:"-"`
	ID          string `gorm:"column:id;size:128;primaryKey" json:"id"`
	Name        string `gorm:"column:name;size:512;not null;index:idx_runbook_template_owner_name,priority:2" json:"name"`
	Description string `gorm:"column:description;type:text;not null" json:"description"`
	Steps       string `gorm:"column:steps;type:text;not null" json:"-"`
	Version     int32  `gorm:"column:version;not null;default:1" json:"version"`
	CreatedAt   int64  `gorm:"column:created_at;not null" json:"createdAt"`
	UpdatedAt   int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
}

func (RunbookTemplateRow) TableName() string { return "runbook_template" }

type ExecBatchResultRow struct {
	OwnerID     string       `gorm:"column:owner_id;size:191;primaryKey;default:'';index:idx_exec_batch_result_batch,priority:1" json:"-"`
	BatchID     string       `gorm:"column:batch_id;size:128;primaryKey;index:idx_exec_batch_result_batch,priority:2" json:"-"`
	SessionID   string       `gorm:"column:session_id;size:191;primaryKey" json:"sessionId"`
	Position    int32        `gorm:"column:position;not null;index:idx_exec_batch_result_batch,priority:3" json:"position"`
	SessionName string       `gorm:"column:session_name;not null;default:''" json:"sessionName"`
	AssetID     string       `gorm:"column:asset_id;size:191;not null;default:''" json:"assetId"`
	Environment string       `gorm:"column:environment;size:32;not null;default:''" json:"environment"`
	Status      string       `gorm:"column:status;size:32;not null" json:"status"`
	ExitCode    *int32       `gorm:"column:exit_code" json:"exitCode"`
	Signal      string       `gorm:"column:signal;size:64;not null;default:''" json:"signal"`
	ErrorKind   string       `gorm:"column:error_kind;size:64;not null;default:''" json:"errorKind"`
	StartedAt   *int64       `gorm:"column:started_at" json:"startedAt"`
	FinishedAt  *int64       `gorm:"column:finished_at" json:"finishedAt"`
	DurationMS  int64        `gorm:"column:duration_ms;not null;default:0" json:"durationMs"`
	Truncated   bool         `gorm:"column:truncated;not null;default:0" json:"truncated"`
	Batch       ExecBatchRow `gorm:"foreignKey:OwnerID,BatchID;references:OwnerID,ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (ExecBatchResultRow) TableName() string { return "exec_batch_result" }

type ExecTargetSetRow struct {
	OwnerID   string `gorm:"column:owner_id;size:191;primaryKey;default:''" json:"-"`
	ID        string `gorm:"column:id;size:128;primaryKey" json:"id"`
	Name      string `gorm:"column:name;size:191;not null" json:"name"`
	CreatedAt int64  `gorm:"column:created_at;not null" json:"createdAt"`
	UpdatedAt int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
}

func (ExecTargetSetRow) TableName() string { return "exec_target_set" }

type ExecTargetSetItemRow struct {
	OwnerID     string           `gorm:"column:owner_id;size:191;primaryKey;default:'';index:idx_exec_target_set_item_set,priority:1" json:"-"`
	TargetSetID string           `gorm:"column:target_set_id;size:128;primaryKey;index:idx_exec_target_set_item_set,priority:2" json:"-"`
	SessionID   string           `gorm:"column:session_id;size:191;primaryKey" json:"sessionId"`
	Position    int32            `gorm:"column:position;not null;index:idx_exec_target_set_item_set,priority:3" json:"position"`
	TargetSet   ExecTargetSetRow `gorm:"foreignKey:OwnerID,TargetSetID;references:OwnerID,ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (ExecTargetSetItemRow) TableName() string { return "exec_target_set_item" }
