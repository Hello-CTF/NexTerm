package model

type AppUser struct {
	ID                 string `gorm:"column:id;primaryKey" json:"id"`
	Username           string `gorm:"column:username;not null" json:"username"`
	DisplayName        string `gorm:"column:display_name;not null;default:''" json:"displayName"`
	Role               string `gorm:"column:role;not null;check:chk_app_user_role,role IN ('superadmin','user')" json:"role"`
	PasswordHash       string `gorm:"column:password_hash;not null" json:"passwordHash"`
	State              string `gorm:"column:state;not null;check:chk_app_user_state,state IN ('active','disabled','reset_required')" json:"state"`
	MustChangePassword bool   `gorm:"column:must_change_password;not null;default:0" json:"mustChangePassword"`
	CreatedAt          int64  `gorm:"column:created_at;not null" json:"createdAt"`
	UpdatedAt          int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
	LastLoginAt        *int64 `gorm:"column:last_login_at" json:"lastLoginAt"`
}

func (AppUser) TableName() string { return "app_user" }

type UserDEK struct {
	UserID           string  `gorm:"column:user_id;primaryKey" json:"userId"`
	DEKEnvelope      []byte  `gorm:"column:dek_envelope;not null" json:"dekEnvelope"`
	KDFSalt          []byte  `gorm:"column:kdf_salt;not null" json:"kdfSalt"`
	KDFParams        string  `gorm:"column:kdf_params;not null" json:"kdfParams"`
	RecoveryEnvelope []byte  `gorm:"column:recovery_envelope;not null" json:"recoveryEnvelope"`
	RecoveryHash     string  `gorm:"column:recovery_hash;not null" json:"recoveryHash"`
	CreatedAt        int64   `gorm:"column:created_at;not null" json:"createdAt"`
	UpdatedAt        int64   `gorm:"column:updated_at;not null" json:"updatedAt"`
	User             AppUser `gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (UserDEK) TableName() string { return "user_dek" }

type UserDevice struct {
	ID         string  `gorm:"column:id;primaryKey" json:"id"`
	UserID     string  `gorm:"column:user_id;not null;index:idx_user_device_user" json:"userId"`
	Name       string  `gorm:"column:name;not null" json:"name"`
	Kind       string  `gorm:"column:kind;not null;default:'desktop'" json:"kind"`
	CreatedAt  int64   `gorm:"column:created_at;not null" json:"createdAt"`
	LastSeenAt *int64  `gorm:"column:last_seen_at" json:"lastSeenAt"`
	RevokedAt  *int64  `gorm:"column:revoked_at" json:"revokedAt"`
	User       AppUser `gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (UserDevice) TableName() string { return "user_device" }

type UserSession struct {
	ID        string      `gorm:"column:id;primaryKey" json:"id"`
	UserID    string      `gorm:"column:user_id;not null;index:idx_user_session_user" json:"userId"`
	DeviceID  *string     `gorm:"column:device_id;index:idx_user_session_device" json:"deviceId"`
	TokenHash string      `gorm:"column:token_hash;not null;uniqueIndex:idx_user_session_token_hash" json:"tokenHash"`
	CreatedAt int64       `gorm:"column:created_at;not null" json:"createdAt"`
	TouchedAt int64       `gorm:"column:touched_at;not null" json:"touchedAt"`
	ExpiresAt int64       `gorm:"column:expires_at;not null" json:"expiresAt"`
	RevokedAt *int64      `gorm:"column:revoked_at" json:"revokedAt"`
	User      AppUser     `gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
	Device    *UserDevice `gorm:"foreignKey:DeviceID;references:ID;constraint:OnDelete:SET NULL" json:"-"`
}

func (UserSession) TableName() string { return "user_session" }

type SyncCredential struct {
	ID         string     `gorm:"column:id;primaryKey" json:"id"`
	UserID     string     `gorm:"column:user_id;not null;index:idx_sync_credential_user" json:"userId"`
	DeviceID   string     `gorm:"column:device_id;not null;index:idx_sync_credential_device" json:"deviceId"`
	Purpose    string     `gorm:"column:purpose;not null" json:"purpose"`
	SecretHash string     `gorm:"column:secret_hash;not null;uniqueIndex:idx_sync_credential_secret_hash" json:"secretHash"`
	CreatedAt  int64      `gorm:"column:created_at;not null" json:"createdAt"`
	ExpiresAt  int64      `gorm:"column:expires_at;not null;default:0" json:"expiresAt"`
	RevokedAt  *int64     `gorm:"column:revoked_at" json:"revokedAt"`
	LastUsedAt *int64     `gorm:"column:last_used_at" json:"lastUsedAt"`
	User       AppUser    `gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
	Device     UserDevice `gorm:"foreignKey:DeviceID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (SyncCredential) TableName() string { return "sync_credential" }

type UserSetting struct {
	UserID    string  `gorm:"column:user_id;primaryKey" json:"userId"`
	Key       string  `gorm:"column:key;primaryKey" json:"key"`
	Value     string  `gorm:"column:value;not null" json:"value"`
	UpdatedAt int64   `gorm:"column:updated_at;not null" json:"updatedAt"`
	User      AppUser `gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (UserSetting) TableName() string { return "user_setting" }

type DeviceEnrollCode struct {
	ID         string  `gorm:"column:id;primaryKey" json:"id"`
	UserID     string  `gorm:"column:user_id;not null;index:idx_device_enroll_code_user" json:"userId"`
	CodeHash   string  `gorm:"column:code_hash;not null;uniqueIndex:idx_device_enroll_code_hash" json:"codeHash"`
	CreatedAt  int64   `gorm:"column:created_at;not null" json:"createdAt"`
	ExpiresAt  int64   `gorm:"column:expires_at;not null" json:"expiresAt"`
	ConsumedAt *int64  `gorm:"column:consumed_at" json:"consumedAt"`
	User       AppUser `gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (DeviceEnrollCode) TableName() string { return "device_enroll_code" }

type UserSyncObject struct {
	UserID   string  `gorm:"column:user_id;primaryKey;index:idx_user_sync_object_seq,priority:1" json:"userId"`
	ID       string  `gorm:"column:id;primaryKey" json:"id"`
	Seq      int64   `gorm:"column:seq;not null;index:idx_user_sync_object_seq,priority:2" json:"seq"`
	Blob     []byte  `gorm:"column:blob;not null" json:"blob"`
	BlobHash []byte  `gorm:"column:blob_hash" json:"blobHash"`
	User     AppUser `gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (UserSyncObject) TableName() string { return "user_sync_object" }

type UserSyncHead struct {
	UserID   string  `gorm:"column:user_id;primaryKey" json:"userId"`
	HeadHash string  `gorm:"column:head_hash;not null" json:"headHash"`
	User     AppUser `gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (UserSyncHead) TableName() string { return "user_sync_head" }

type DeviceAgent struct {
	DeviceID         string         `gorm:"column:device_id;primaryKey" json:"deviceId"`
	CredentialID     string         `gorm:"column:credential_id;not null" json:"credentialId"`
	Platform         string         `gorm:"column:platform;not null;default:''" json:"platform"`
	AppVersion       string         `gorm:"column:app_version;not null;default:''" json:"appVersion"`
	DesiredAutostart bool           `gorm:"column:desired_autostart;not null;default:1" json:"desiredAutostart"`
	TerminalEnabled  bool           `gorm:"column:terminal_enabled;not null;default:1" json:"terminalEnabled"`
	CurrentURL       string         `gorm:"column:current_url;not null;default:''" json:"currentUrl"`
	ServiceStateJSON string         `gorm:"column:service_state_json;not null;default:'{}'" json:"serviceStateJson"`
	CreatedAt        int64          `gorm:"column:created_at;not null" json:"createdAt"`
	LastSeenAt       *int64         `gorm:"column:last_seen_at" json:"lastSeenAt"`
	Device           UserDevice     `gorm:"foreignKey:DeviceID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
	Credential       SyncCredential `gorm:"foreignKey:CredentialID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (DeviceAgent) TableName() string { return "device_agent" }

type DeviceMetrics struct {
	ID        string     `gorm:"column:id;primaryKey" json:"id"`
	DeviceID  string     `gorm:"column:device_id;not null;index:idx_device_metrics_device_ts,priority:1" json:"deviceId"`
	TS        int64      `gorm:"column:ts;not null;index:idx_device_metrics_device_ts,priority:2" json:"ts"`
	CPUPct    *float64   `gorm:"column:cpu_pct" json:"cpuPct"`
	MemUsed   *int64     `gorm:"column:mem_used" json:"memUsed"`
	MemTotal  *int64     `gorm:"column:mem_total" json:"memTotal"`
	DiskUsed  *int64     `gorm:"column:disk_used" json:"diskUsed"`
	DiskTotal *int64     `gorm:"column:disk_total" json:"diskTotal"`
	UptimeS   *int64     `gorm:"column:uptime_s" json:"uptimeS"`
	Device    UserDevice `gorm:"foreignKey:DeviceID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (DeviceMetrics) TableName() string { return "device_metrics" }

type DeviceMetricsHourly struct {
	DeviceID    string     `gorm:"column:device_id;primaryKey" json:"deviceId"`
	BucketTS    int64      `gorm:"column:bucket_ts;primaryKey" json:"bucketTs"`
	CPUPct      *float64   `gorm:"column:cpu_pct" json:"cpuPct"`
	MemUsed     *int64     `gorm:"column:mem_used" json:"memUsed"`
	MemTotal    *int64     `gorm:"column:mem_total" json:"memTotal"`
	DiskUsed    *int64     `gorm:"column:disk_used" json:"diskUsed"`
	DiskTotal   *int64     `gorm:"column:disk_total" json:"diskTotal"`
	UptimeS     *int64     `gorm:"column:uptime_s" json:"uptimeS"`
	SampleCount int64      `gorm:"column:sample_count;not null" json:"sampleCount"`
	Device      UserDevice `gorm:"foreignKey:DeviceID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (DeviceMetricsHourly) TableName() string { return "device_metrics_hourly" }

type UserTOTP struct {
	UserID                string  `gorm:"column:user_id;primaryKey" json:"userId"`
	SecretEnvelope        []byte  `gorm:"column:secret_envelope" json:"secretEnvelope"`
	PendingSecretEnvelope []byte  `gorm:"column:pending_secret_envelope" json:"pendingSecretEnvelope"`
	CreatedAt             int64   `gorm:"column:created_at;not null" json:"createdAt"`
	UpdatedAt             int64   `gorm:"column:updated_at;not null" json:"updatedAt"`
	User                  AppUser `gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (UserTOTP) TableName() string { return "user_totp" }

type UserTOTPRecoveryCode struct {
	ID        string  `gorm:"column:id;primaryKey" json:"id"`
	UserID    string  `gorm:"column:user_id;not null;index:idx_user_totp_recovery_code_user" json:"userId"`
	CodeHash  string  `gorm:"column:code_hash;not null" json:"codeHash"`
	CreatedAt int64   `gorm:"column:created_at;not null" json:"createdAt"`
	UsedAt    *int64  `gorm:"column:used_at" json:"usedAt"`
	User      AppUser `gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (UserTOTPRecoveryCode) TableName() string { return "user_totp_recovery_code" }
