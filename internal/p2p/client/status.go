package client

// Status is the contract's DevicePathStatus: where the device's live streams run and why not directly.
type Status struct {
	DeviceID string `json:"deviceId"`
	Mode     string `json:"mode"`
	Path     string `json:"path"`
	Reason   string `json:"reason"`
	// Since is the Unix millisecond time of the last path change.
	Since int64 `json:"since"`
}

// Switch is one recorded path change.
type Switch struct {
	At     int64  `json:"at"`
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// SelectedPair is the ICE pair a direct leg uses.
type SelectedPair struct {
	Local  string `json:"local"`
	Remote string `json:"remote"`
	Type   string `json:"type"`
}

// Policy reports each policy layer: the server switch, whether the device accepts direct connections (its key
// is bound) and the stored mode.
type Policy struct {
	Server bool   `json:"server"`
	Device bool   `json:"device"`
	Mode   string `json:"mode"`
}

// Diagnostics is the contract's DeviceP2PDiagnostics.
type Diagnostics struct {
	Status         Status        `json:"status"`
	LocalNAT       string        `json:"localNat"`
	RemoteNAT      string        `json:"remoteNat"`
	STUNReachable  bool          `json:"stunReachable"`
	SelectedPair   *SelectedPair `json:"selectedPair,omitempty"`
	DirectRTTMS    *float64      `json:"directRttMs,omitempty"`
	RelayRTTMS     *float64      `json:"relayRttMs,omitempty"`
	LossPct        *float64      `json:"lossPct,omitempty"`
	RecentSwitches []Switch      `json:"recentSwitches"`
	Policy         Policy        `json:"policy"`
}
