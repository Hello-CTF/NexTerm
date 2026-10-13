// Package deviceproto is the wire protocol spoken between the device
// transport client and the agent-side purpose servers over one fleet bridge
// connection. A frame is a 4-byte big-endian payload length, a 1-byte frame
// type, and the payload: JSON for control messages, raw bytes for bulk data.
// One bridge connection carries exactly one operation: the client dials a
// fresh purpose bridge per operation, sends one request, and reads reply
// frames until the operation completes; streaming operations (open read,
// open write, dial) keep their bridge until the stream ends.
package deviceproto

const (
	// PurposeFile serves filesystem operations on the device.
	PurposeFile = "file"
	// PurposeForward dials a TCP/unix target from the device and pipes bytes.
	PurposeForward = "forward"
	// PurposeTerminal is the supervisor terminal bridge; it predates this
	// package and is restated here so the transport passes one vocabulary.
	PurposeTerminal = "terminal"
)

const (
	// MaxFramePayload bounds one frame; data moves in DataChunkSize pieces.
	MaxFramePayload = 256 * 1024
	// DataChunkSize is the payload size of one bulk data frame.
	DataChunkSize = 32 * 1024
	// MaxWriteFileSize bounds the buffered payload of one WriteFile request;
	// larger saves belong to the streaming OpenWrite path.
	MaxWriteFileSize = 64 << 20
	// MaxReadBytes bounds one ReadFile/ReadRange window; both are buffered in
	// agent memory, so an uncapped MaxBytes would let any bridge client OOM the agent.
	MaxReadBytes = 64 << 20
)
