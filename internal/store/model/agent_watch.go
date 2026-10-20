package model

// AgentWatchRow is the durable state of one agent watch condition. The watch
// binds to a target stream position (AnchorSeq plus the bounded CarryBlob
// overlap that preserves partial matches across chunk boundaries and
// restarts) and to a target incarnation (Identity plus the durable target
// Generation); evaluation only advances through CAS on Revision. Status is
// the watch condition state machine: armed -> matched | cancelled | expired |
// unverifiable, where matched is terminal unless Repeat re-arms it on event
// acknowledgement.
type AgentWatchRow struct {
	ID            string `gorm:"column:id;primaryKey;size:191" json:"id"`
	OwnerID       string `gorm:"size:191;column:owner_id;not null;default:'';index:idx_agent_watch_owner" json:"owner_id"`
	TargetID      string `gorm:"size:191;column:target_id;not null;default:'';index:idx_agent_watch_target_status,priority:1" json:"target_id"`
	Identity      string `gorm:"size:191;column:identity;not null;default:''" json:"identity"`
	Generation    uint64 `gorm:"column:generation;not null;default:1" json:"generation"`
	Name          string `gorm:"column:name;not null;default:''" json:"name"`
	ConditionJSON string `gorm:"column:condition_json;not null;default:'{}'" json:"condition_json"`
	Status        string `gorm:"size:32;column:status;not null;index:idx_agent_watch_target_status,priority:2" json:"status"`
	Repeat        bool   `gorm:"column:repeat;not null;default:0" json:"repeat"`
	AnchorSeq     int64  `gorm:"column:anchor_seq;not null;default:0" json:"anchor_seq"`
	CarryBlob     []byte `gorm:"column:carry_blob" json:"carry_blob"`
	FireSeq       int64  `gorm:"column:fire_seq;not null;default:0" json:"fire_seq"`
	Revision      uint64 `gorm:"column:revision;not null;default:1" json:"revision"`
	DeliveryID    string `gorm:"size:191;column:delivery_id;not null;default:''" json:"delivery_id"`
	RunID         string `gorm:"size:191;column:run_id;not null;default:''" json:"run_id"`
	MatchedFrom   int64  `gorm:"column:matched_from;not null;default:0" json:"matched_from"`
	MatchedTo     int64  `gorm:"column:matched_to;not null;default:0" json:"matched_to"`
	Excerpt       string `gorm:"column:excerpt;not null;default:''" json:"excerpt"`
	LastError     string `gorm:"column:last_error;not null;default:''" json:"last_error"`
	CreatedAt     int64  `gorm:"column:created_at;not null" json:"created_at"`
	UpdatedAt     int64  `gorm:"column:updated_at;not null" json:"updated_at"`
	ExpiresAt     *int64 `gorm:"column:expires_at" json:"expires_at"`
}

func (AgentWatchRow) TableName() string { return "agent_watch" }

// AgentWatchTargetRow is the durable lifecycle state of one watched target
// per owner. Identity is the currently attached incarnation, Generation
// increments on every identity replacement or permanent detach, and Gone
// records a completed permanent detach so a stale detach or create can be
// rejected after a restart. Watch creation and permanent invalidation
// serialize on this row, so a create can never escape a concurrent
// invalidation.
type AgentWatchTargetRow struct {
	TargetID   string `gorm:"column:target_id;primaryKey;size:191" json:"target_id"`
	OwnerID    string `gorm:"size:191;column:owner_id;primaryKey;default:''" json:"owner_id"`
	Identity   string `gorm:"size:191;column:identity;not null;default:''" json:"identity"`
	Generation uint64 `gorm:"column:generation;not null;default:1" json:"generation"`
	Gone       bool   `gorm:"column:gone;not null;default:0" json:"gone"`
	UpdatedAt  int64  `gorm:"column:updated_at;not null" json:"updated_at"`
}

func (AgentWatchTargetRow) TableName() string { return "agent_watch_target" }

// AgentWatchOutboxRow is the durable delivery outbox for watch events. One
// committed fire produces exactly one row in the same transaction as the
// watch transition; DeliveryID is the fixed idempotency key for the logical
// fire and never changes across retries, redelivery or restarts. Delivery is
// at-least-once, so consumers must dedupe by DeliveryID.
type AgentWatchOutboxRow struct {
	DeliveryID  string `gorm:"column:delivery_id;primaryKey;size:191" json:"delivery_id"`
	RunID       string `gorm:"size:191;column:run_id;not null" json:"run_id"`
	WatchID     string `gorm:"size:191;column:watch_id;not null;index:idx_agent_watch_outbox_watch;uniqueIndex:idx_agent_watch_outbox_fire,priority:1" json:"watch_id"`
	FireSeq     int64  `gorm:"column:fire_seq;not null;uniqueIndex:idx_agent_watch_outbox_fire,priority:2" json:"fire_seq"`
	OwnerID     string `gorm:"size:191;column:owner_id;not null;default:'';index:idx_agent_watch_outbox_owner" json:"owner_id"`
	Kind        string `gorm:"size:32;column:kind;not null" json:"kind"`
	Status      string `gorm:"size:16;column:status;not null;default:'pending';index:idx_agent_watch_outbox_status" json:"status"`
	Revision    uint64 `gorm:"column:revision;not null;default:1" json:"revision"`
	EventJSON   string `gorm:"column:event_json;not null" json:"event_json"`
	CreatedAt   int64  `gorm:"column:created_at;not null" json:"created_at"`
	UpdatedAt   int64  `gorm:"column:updated_at;not null" json:"updated_at"`
	DeliveredAt *int64 `gorm:"column:delivered_at" json:"delivered_at"`
}

func (AgentWatchOutboxRow) TableName() string { return "agent_watch_outbox" }
