package model

// P2PSigningKey is a server Ed25519 key that signs direct-connection grants. Every server instance sharing the
// database signs with the newest key and publishes the recent ones, so rotation needs no coordination.
type P2PSigningKey struct {
	KID string `gorm:"size:64;column:kid;primaryKey" json:"kid"`
	Alg string `gorm:"size:32;column:alg;not null" json:"alg"`
	// Seed is the 32-byte Ed25519 private key seed.
	Seed      []byte `gorm:"column:seed;not null" json:"-"`
	PublicKey string `gorm:"size:64;column:public_key;not null" json:"publicKey"`
	CreatedAt int64  `gorm:"column:created_at;not null;index:idx_p2p_signing_key_created" json:"createdAt"`
}

func (P2PSigningKey) TableName() string { return "p2p_signing_key" }

// DeviceP2PKey is the Ed25519 identity key a device agent registered for direct connections. The first key is
// trusted on first use; a different key is held in PendingKey, and direct connections stay off until the owner
// confirms it.
type DeviceP2PKey struct {
	DeviceID  string `gorm:"size:191;column:device_id;primaryKey" json:"deviceId"`
	PublicKey string `gorm:"size:64;column:public_key;not null" json:"publicKey"`
	// PendingKey is empty unless the agent presented a key other than PublicKey.
	PendingKey  string     `gorm:"size:64;column:pending_key;not null;default:''" json:"pendingKey"`
	PendingAt   *int64     `gorm:"column:pending_at" json:"pendingAt"`
	BoundAt     int64      `gorm:"column:bound_at;not null" json:"boundAt"`
	ConfirmedAt *int64     `gorm:"column:confirmed_at" json:"confirmedAt"`
	Device      UserDevice `gorm:"foreignKey:DeviceID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (DeviceP2PKey) TableName() string { return "device_p2p_key" }
