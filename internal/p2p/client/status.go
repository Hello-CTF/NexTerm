package client

import "github.com/Hello-CTF/NexTerm/internal/fleet/agent"

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

// Policy layer states. Unknown means the layer could not be read, which is not the same as off.
const (
	PolicyOn      = "on"
	PolicyOff     = "off"
	PolicyUnknown = "unknown"
)

// P2PPolicy reports each policy layer: the server switch, whether the device accepts direct connections (its
// key is bound), the device key state behind that and the stored mode.
type P2PPolicy struct {
	Server string `json:"server"`
	Device string `json:"device"`
	// DeviceKey is the key state the server reports ("bound", "pending", "none"); "" when the device has no
	// registered key or the state could not be read.
	DeviceKey string `json:"deviceKey"`
	Mode      string `json:"mode"`
}

func serverPolicy(config *agent.P2PConfig) string {
	if config.Enabled {
		return PolicyOn
	}
	return PolicyOff
}

// devicePolicy maps a key state the server reported; a device without a reported key has none bound.
func devicePolicy(state string) string {
	if state == agent.P2PKeyStateBound {
		return PolicyOn
	}
	return PolicyOff
}

// DeviceP2PDiagnostics is the device's direct-connection diagnostics (wave 2 contract).
type DeviceP2PDiagnostics struct {
	Status    DevicePathStatus `json:"status"`
	LocalNAT  string           `json:"localNat"`
	RemoteNAT string           `json:"remoteNat"`
	// Probed reports a NAT probe against at least one STUN server; until then the NAT and STUN fields hold no
	// measurement.
	Probed         bool             `json:"probed"`
	STUNReachable  bool             `json:"stunReachable"`
	SelectedPair   *P2PSelectedPair `json:"selectedPair,omitempty"`
	DirectRTTMS    *float64         `json:"directRttMs,omitempty"`
	RelayRTTMS     *float64         `json:"relayRttMs,omitempty"`
	LossPct        *float64         `json:"lossPct,omitempty"`
	RecentSwitches []PathSwitch     `json:"recentSwitches"`
	Policy         P2PPolicy        `json:"policy"`
}
