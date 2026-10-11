package model

type ShareLink struct {
	ID             string     `gorm:"column:id;primaryKey" json:"id"`
	OwnerID        string     `gorm:"column:owner_id;not null;index:idx_share_link_owner" json:"ownerId"`
	DeviceID       string     `gorm:"column:device_id;not null;index:idx_share_link_device" json:"deviceId"`
	SessionID      string     `gorm:"column:session_id;not null" json:"sessionId"`
	TokenHash      string     `gorm:"column:token_hash;not null;uniqueIndex:idx_share_link_token_hash" json:"tokenHash"`
	Permission     string     `gorm:"column:permission;not null;default:'read'" json:"permission"`
	CreatedAt      int64      `gorm:"column:created_at;not null" json:"createdAt"`
	ExpiresAt      int64      `gorm:"column:expires_at;not null" json:"expiresAt"`
	RevokedAt      *int64     `gorm:"column:revoked_at" json:"revokedAt"`
	LastAccessedAt *int64     `gorm:"column:last_accessed_at" json:"lastAccessedAt"`
	Owner          AppUser    `gorm:"foreignKey:OwnerID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
	Device         UserDevice `gorm:"foreignKey:DeviceID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (ShareLink) TableName() string { return "share_link" }

type SharePreviewLink struct {
	ID             string  `gorm:"column:id;primaryKey" json:"id"`
	OwnerID        string  `gorm:"column:owner_id;not null;index:idx_share_preview_link_owner" json:"ownerId"`
	SessionID      string  `gorm:"column:session_id;not null" json:"sessionId"`
	TokenHash      string  `gorm:"column:token_hash;not null;uniqueIndex:idx_share_preview_token_hash" json:"tokenHash"`
	CreatedAt      int64   `gorm:"column:created_at;not null" json:"createdAt"`
	ExpiresAt      int64   `gorm:"column:expires_at;not null" json:"expiresAt"`
	RevokedAt      *int64  `gorm:"column:revoked_at" json:"revokedAt"`
	LastAccessedAt *int64  `gorm:"column:last_accessed_at" json:"lastAccessedAt"`
	Owner          AppUser `gorm:"foreignKey:OwnerID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (SharePreviewLink) TableName() string { return "share_preview_link" }

type HostShare struct {
	ID          string     `gorm:"column:id;primaryKey" json:"id"`
	OwnerID     string     `gorm:"column:owner_id;not null;uniqueIndex:host_share_owner_device_recipient_unique,priority:1" json:"ownerId"`
	DeviceID    string     `gorm:"column:device_id;not null;index:idx_host_share_device;uniqueIndex:host_share_owner_device_recipient_unique,priority:2" json:"deviceId"`
	RecipientID string     `gorm:"column:recipient_id;not null;index:idx_host_share_recipient;uniqueIndex:host_share_owner_device_recipient_unique,priority:3" json:"recipientId"`
	Permission  string     `gorm:"column:permission;not null;default:'read_write'" json:"permission"`
	CreatedAt   int64      `gorm:"column:created_at;not null" json:"createdAt"`
	ExpiresAt   int64      `gorm:"column:expires_at;not null" json:"expiresAt"`
	RevokedAt   *int64     `gorm:"column:revoked_at" json:"revokedAt"`
	Owner       AppUser    `gorm:"foreignKey:OwnerID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
	Device      UserDevice `gorm:"foreignKey:DeviceID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
	Recipient   AppUser    `gorm:"foreignKey:RecipientID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (HostShare) TableName() string { return "host_share" }

type CommandLog struct {
	ID         int64   `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	SessionID  string  `gorm:"column:session_id;not null;index:idx_command_log_session" json:"sessionId"`
	TabID      string  `gorm:"column:tab_id;not null" json:"tabId"`
	AssetID    string  `gorm:"column:asset_id;not null;index:idx_command_log_asset" json:"assetId"`
	UserID     *string `gorm:"column:user_id;index:idx_command_log_user" json:"userId"`
	Command    string  `gorm:"column:command;not null" json:"command"`
	Source     string  `gorm:"column:source;not null" json:"source"`
	ExitCode   *int32  `gorm:"column:exit_code" json:"exitCode"`
	StartedAt  int64   `gorm:"column:started_at;not null;index:idx_command_log_started,sort:desc" json:"startedAt"`
	FinishedAt int64   `gorm:"column:finished_at;not null" json:"finishedAt"`
}

func (CommandLog) TableName() string { return "command_log" }

type DeviceShareLink struct {
	ID             string     `gorm:"column:id;primaryKey" json:"id"`
	OwnerID        string     `gorm:"column:owner_id;not null;index:idx_device_share_link_owner" json:"ownerId"`
	DeviceID       string     `gorm:"column:device_id;not null;index:idx_device_share_link_device" json:"deviceId"`
	TokenHash      string     `gorm:"column:token_hash;not null;uniqueIndex:idx_device_share_token_hash" json:"tokenHash"`
	Permission     string     `gorm:"column:permission;not null;default:'read'" json:"permission"`
	CreatedAt      int64      `gorm:"column:created_at;not null" json:"createdAt"`
	NotBefore      int64      `gorm:"column:not_before;not null;default:0" json:"notBefore"`
	ExpiresAt      int64      `gorm:"column:expires_at;not null" json:"expiresAt"`
	RevokedAt      *int64     `gorm:"column:revoked_at" json:"revokedAt"`
	LastAccessedAt *int64     `gorm:"column:last_accessed_at" json:"lastAccessedAt"`
	Owner          AppUser    `gorm:"foreignKey:OwnerID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
	Device         UserDevice `gorm:"foreignKey:DeviceID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (DeviceShareLink) TableName() string { return "device_share_link" }
