package client

// DevicePathStatus is where the device's live streams run and why not directly (wave 2 contract).
type DevicePathStatus struct {
	DeviceID string `json:"deviceId"`
	Mode     string `json:"mode"`
	Path     string `json:"path"`
	Reason   string `json:"reason"`
	// Since is the Unix millisecond time of the last path change.
	Since int64 `json:"since"`
}

// PathSwitch is one recorded path change.
type PathSwitch struct {
	At     int64  `json:"at"`
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// P2PSelectedPair is the ICE pair a direct leg uses.
type P2PSelectedPair struct {
	Local  string `json:"local"`
	Remote string `json:"remote"`
	Type   string `json:"type"`
}

// P2PPolicy reports each policy layer: the server switch, whether the device accepts direct connections (its
// key is bound) and the stored mode.
type P2PPolicy struct {
	Server bool   `json:"server"`
	Device bool   `json:"device"`
	Mode   string `json:"mode"`
}

// DeviceP2PDiagnostics is the device's direct-connection diagnostics (wave 2 contract).
type DeviceP2PDiagnostics struct {
	Status         DevicePathStatus `json:"status"`
	LocalNAT       string           `json:"localNat"`
	RemoteNAT      string           `json:"remoteNat"`
	STUNReachable  bool             `json:"stunReachable"`
	SelectedPair   *P2PSelectedPair `json:"selectedPair,omitempty"`
	DirectRTTMS    *float64         `json:"directRttMs,omitempty"`
	RelayRTTMS     *float64         `json:"relayRttMs,omitempty"`
	LossPct        *float64         `json:"lossPct,omitempty"`
	RecentSwitches []PathSwitch     `json:"recentSwitches"`
	Policy         P2PPolicy        `json:"policy"`
}
