package rstream

// Relay negotiation. An agent that unwraps rstream carriers advertises Capability (which also means it adds the
// SSH session variables to its terminals itself, so the server never rewrites a stream's bytes). A client asks
// for a stream bridge with RelayStreamParam=1 on the relay URL and RelayResumeParam=1 when the carrier is for a
// RESUME. The server answers the websocket upgrade with RelayStreamHeader=1 only when the agent will read the
// bridge as a carrier; without the header the bridge is raw, as with servers and agents that predate streams.
const (
	Capability        = "rstream"
	RelayStreamParam  = "stream"
	RelayResumeParam  = "resume"
	RelayStreamHeader = "X-Nexterm-Stream"
)
