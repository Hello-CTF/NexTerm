package supervisor

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ProtocolVersion is the only protocol a helper and its clients speak. Version
// 3 folded every capability earlier versions negotiated per feature (exec,
// metadata, compressed replay and output, runtime metadata, activity,
// create-then-attach, windowed input and output credit) into the baseline.
// Helpers of different versions listen on different endpoints, so a version 3
// helper never sees the sessions of an older one.
const ProtocolVersion = 3

const (
	maxFramePayload = 256 * 1024
	outputChunkSize = 32 * 1024
)

type frameType byte

const (
	frameHello frameType = 1 + iota
	frameHelloAck
	frameError
	frameCreate
	frameCreated
	frameList
	frameListed
	frameAttach
	frameAttached
	frameInput
	frameInputAck
	frameResize
	frameOK
	frameKill
	frameGetVersions
	frameVersions
	frameSetVersions
	frameDetach
	frameOutput
	frameExit
	frameFatal
	frameKillSession
	frameExecOutput
	frameRename
	frameRenamed
	frameReplayGzip
	// frameCredit carries the client's cumulative count of retired output
	// bytes (8-byte big-endian). It has no reply.
	frameCredit
	// frameOutputZstd is a live output chunk compressed per frame; see
	// output_zstd.go. Only sent to clients that asked for compressed output.
	frameOutputZstd
)

// Every connection uses windowed input and credited output. The client may keep
// several frameInput frames in flight; the server acknowledges each one in
// order with an inputAckMsg that may carry an error, and delays the
// acknowledgement while the session's input queue is full instead of rejecting
// the frame. The server pauses streaming the recording once
// outputCreditWindow bytes are unretired; the client returns credit with
// frameCredit as its consumer reads.
const (
	// inputWindowBytes and inputWindowFrames bound the unacknowledged input a
	// client may have in flight; the server tolerates a small multiple of it.
	inputWindowBytes  = 1 << 20
	inputWindowFrames = 256
	// outputCreditWindow is the most unretired output the server streams ahead.
	outputCreditWindow = 4 << 20
	// outputCreditStep is how much the client retires before returning credit.
	outputCreditStep = outputCreditWindow / 8
)

const (
	serverInputMaxFrames = 4 * inputWindowFrames
	serverInputMaxBytes  = 4 * inputWindowBytes
)

// Exec output stream discriminators, the first byte of a frameExecOutput
// payload.
const (
	streamStdout byte = 1
	streamStderr byte = 2
)

type helloMsg struct {
	Version     int    `json:"version"`
	StateDigest string `json:"state_digest,omitempty"`
	// CompressOutput is a client preference, not a capability: remote clients
	// ask for gzip replay batches and per-frame zstd live output, local clients
	// keep raw frames to save CPU.
	CompressOutput bool `json:"compress_output,omitempty"`
	// Generation, Runtime and Activity are sent by the helper in its
	// acknowledgement. Generation identifies the helper process.
	Generation string `json:"generation,omitempty"`
	// Runtime is absent only when the helper could not hash its executable.
	Runtime  *RuntimeMetadata  `json:"runtime,omitempty"`
	Activity *ActivitySnapshot `json:"activity,omitempty"`
}

type createMsg struct {
	ID      string   `json:"id,omitempty"`
	Attempt string   `json:"attempt,omitempty"`
	Command []string `json:"command,omitempty"`
	Dir     string   `json:"dir,omitempty"`
	Env     []string `json:"env,omitempty"`
	Cols    uint32   `json:"cols,omitempty"`
	Rows    uint32   `json:"rows,omitempty"`
	// Pty is nil or true for a PTY session; false requests a one-shot non-PTY
	// exec session.
	Pty *bool `json:"pty,omitempty"`
}

type attachMsg struct {
	ID                string `json:"id"`
	ExpectCreatedAt   int64  `json:"expect_created_at_unix_nano,omitempty"`
	ExpectIncarnation string `json:"expect_incarnation,omitempty"`
	// ExpectGeneration, when set, rejects the attach with
	// codeGenerationMismatch unless this helper process is the one the client
	// observed the session in.
	ExpectGeneration string `json:"expect_generation,omitempty"`
	// Offset is the absolute byte offset of the recording the client has
	// consumed; the server seeks there and resumes.
	Offset uint64 `json:"offset,omitempty"`
}

type killSessionMsg struct {
	ID                string `json:"id"`
	Attempt           string `json:"attempt,omitempty"`
	ExpectCreatedAt   int64  `json:"expect_created_at_unix_nano,omitempty"`
	ExpectIncarnation string `json:"expect_incarnation,omitempty"`
}

// resizeMsg and every frameInput carry the helper generation and session
// incarnation the client attached to; the helper rejects a mismatch instead of
// applying the request to another process or session.
type resizeMsg struct {
	Cols              uint32 `json:"cols"`
	Rows              uint32 `json:"rows"`
	ExpectGeneration  string `json:"expect_generation"`
	ExpectIncarnation string `json:"expect_incarnation"`
}

type versionsMsg struct {
	Event uint64 `json:"event"`
	Grid  uint64 `json:"grid"`
}

type inputAckMsg struct {
	Written int `json:"written"`
	// Error is set when the frame was rejected; the stream's later writes
	// surface it.
	Error *errorMsg `json:"error,omitempty"`
}

type errorMsg struct {
	Code             string `json:"code"`
	Message          string `json:"message"`
	ID               string `json:"id,omitempty"`
	Title            string `json:"title,omitempty"`
	MetadataRevision uint64 `json:"metadata_revision,omitempty"`
	// Version accompanies codeVersionMismatch: the protocol the rejecting
	// helper speaks. Helpers before version 3 leave it empty.
	Version int `json:"version,omitempty"`
}

type exitMsg struct {
	Code   *int   `json:"code,omitempty"`
	Signal string `json:"signal,omitempty"`
}

const (
	codeInternal           = "internal"
	codeNotFound           = "not_found"
	codeAlreadyExists      = "already_exists"
	codeIdentity           = "identity"
	codeMetadataConflict   = "metadata_conflict"
	codeExited             = "exited"
	codeInvalidInput       = "invalid_input"
	codeUnavailable        = "unavailable"
	codeClosed             = "closed"
	codeProtocol           = "protocol"
	codeVersionMismatch    = "version_mismatch"
	codeStateMismatch      = "state_mismatch"
	codeGenerationMismatch = "generation_mismatch"
)

func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return codeNotFound
	case errors.Is(err, ErrAlreadyExists):
		return codeAlreadyExists
	case errors.Is(err, ErrIdentity):
		return codeIdentity
	case errors.Is(err, ErrMetadataConflict):
		return codeMetadataConflict
	case errors.Is(err, ErrExited):
		return codeExited
	case errors.Is(err, ErrInvalidInput):
		return codeInvalidInput
	case errors.Is(err, ErrUnavailable):
		return codeUnavailable
	case errors.Is(err, ErrClosed):
		return codeClosed
	case errors.Is(err, ErrProtocol):
		return codeProtocol
	case errors.Is(err, ErrStateMismatch):
		return codeStateMismatch
	case errors.Is(err, ErrGenerationMismatch):
		return codeGenerationMismatch
	case errors.Is(err, ErrUnsupported):
		return codeProtocol
	default:
		return codeInternal
	}
}

type wireError struct {
	error
}

func (e *wireError) Unwrap() error {
	return e.error
}

func errorMessage(err error) errorMsg {
	msg := errorMsg{Code: errorCode(err), Message: err.Error()}
	var conflict *MetadataConflictError
	if errors.As(err, &conflict) {
		msg.ID = conflict.Current.ID
		msg.Title = conflict.Current.Title
		msg.MetadataRevision = conflict.Current.MetadataRevision
	}
	return msg
}

func codedError(code, message string) error {
	sentinel := errorSentinel(code)
	if message == "" {
		return &wireError{sentinel}
	}
	return &wireError{fmt.Errorf("%w: %s", sentinel, message)}
}

func decodedError(msg errorMsg) error {
	if msg.Code == codeVersionMismatch {
		return &wireError{versionMismatchError(msg.Version, msg.Message)}
	}
	if msg.Code == codeMetadataConflict {
		return &wireError{&MetadataConflictError{
			Current: Info{ID: msg.ID, Title: msg.Title, MetadataRevision: msg.MetadataRevision},
			message: msg.Message,
		}}
	}
	return codedError(msg.Code, msg.Message)
}

func errorSentinel(code string) error {
	switch code {
	case codeNotFound:
		return ErrNotFound
	case codeAlreadyExists:
		return ErrAlreadyExists
	case codeIdentity:
		return ErrIdentity
	case codeMetadataConflict:
		return ErrMetadataConflict
	case codeExited:
		return ErrExited
	case codeInvalidInput:
		return ErrInvalidInput
	case codeUnavailable:
		return ErrUnavailable
	case codeClosed:
		return ErrClosed
	case codeProtocol:
		return ErrProtocol
	case codeVersionMismatch:
		return ErrVersionMismatch
	case codeStateMismatch:
		return ErrStateMismatch
	case codeGenerationMismatch:
		return ErrGenerationMismatch
	default:
		return fmt.Errorf("%w: remote error %q", ErrUnavailable, code)
	}
}

func writeFrame(writer io.Writer, kind frameType, payload []byte) error {
	if len(payload) > maxFramePayload {
		return fmt.Errorf("%w: frame payload of %d bytes exceeds limit", ErrProtocol, len(payload))
	}
	header := make([]byte, 5)
	binary.BigEndian.PutUint32(header, uint32(len(payload)))
	header[4] = byte(kind)
	frame := append(header, payload...)
	for len(frame) > 0 {
		count, err := writer.Write(frame)
		if err != nil {
			return err
		}
		frame = frame[count:]
	}
	return nil
}

func readFrame(reader io.Reader) (frameType, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header)
	if length > maxFramePayload {
		return 0, nil, fmt.Errorf("%w: frame payload of %d bytes exceeds limit", ErrProtocol, length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	return frameType(header[4]), payload, nil
}

func marshalFrame(kind frameType, message any) ([]byte, error) {
	payload, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("%w: encode frame: %v", ErrProtocol, err)
	}
	return payload, nil
}

func unmarshalFrame(payload []byte, message any) error {
	if err := json.Unmarshal(payload, message); err != nil {
		return fmt.Errorf("%w: malformed frame payload: %v", ErrProtocol, err)
	}
	return nil
}

func outputPayload(seq uint64, data []byte) []byte {
	payload := make([]byte, 8+len(data))
	binary.BigEndian.PutUint64(payload, seq)
	copy(payload[8:], data)
	return payload
}

func parseOutput(payload []byte) (seq uint64, data []byte, err error) {
	if len(payload) < 8 {
		return 0, nil, fmt.Errorf("%w: output frame of %d bytes is too short", ErrProtocol, len(payload))
	}
	return binary.BigEndian.Uint64(payload), payload[8:], nil
}

// maxInputHeader bounds the expectation header of a frameInput payload: two
// length-prefixed IDs.
const maxInputHeader = 2 * (1 + 255)

// maxInputData is the most input one frame carries.
const maxInputData = maxFramePayload - maxInputHeader

// inputPayload prefixes input data with the helper generation and session
// incarnation it is meant for, each as a one-byte length and the ID bytes.
func inputPayload(generation, incarnation string, data []byte) ([]byte, error) {
	if len(generation) > 255 || len(incarnation) > 255 {
		return nil, fmt.Errorf("%w: input expectation is too long", ErrInvalidInput)
	}
	payload := make([]byte, 0, 2+len(generation)+len(incarnation)+len(data))
	payload = append(payload, byte(len(generation)))
	payload = append(payload, generation...)
	payload = append(payload, byte(len(incarnation)))
	payload = append(payload, incarnation...)
	return append(payload, data...), nil
}

func parseInput(payload []byte) (generation, incarnation string, data []byte, err error) {
	fields := [2]string{}
	for index := range fields {
		if len(payload) < 1 || len(payload) < 1+int(payload[0]) {
			return "", "", nil, fmt.Errorf("%w: truncated input frame header", ErrProtocol)
		}
		size := int(payload[0])
		fields[index] = string(payload[1 : 1+size])
		payload = payload[1+size:]
	}
	return fields[0], fields[1], payload, nil
}

func execOutputPayload(stream byte, data []byte) []byte {
	payload := make([]byte, 1+len(data))
	payload[0] = stream
	copy(payload[1:], data)
	return payload
}

func parseExecOutput(payload []byte) (stream byte, data []byte, err error) {
	if len(payload) < 1 {
		return 0, nil, fmt.Errorf("%w: exec output frame is empty", ErrProtocol)
	}
	return payload[0], payload[1:], nil
}

// versionMismatchError turns a helper's version rejection into the error that
// says which side needs upgrading. helperVersion is zero when the helper is
// older than version 3 and did not report its version.
func versionMismatchError(helperVersion int, message string) error {
	if helperVersion > ProtocolVersion {
		return fmt.Errorf("%w: the supervisor helper speaks protocol %d but this client only speaks protocol %d; update this client (%s)", ErrClientUpgradeRequired, helperVersion, ProtocolVersion, message)
	}
	spoken := "an older protocol"
	if helperVersion > 0 {
		spoken = fmt.Sprintf("protocol %d", helperVersion)
	}
	return fmt.Errorf("%w: the supervisor helper speaks %s but this client requires protocol %d; sessions of the older helper stay with it until it is upgraded (%s)", ErrHelperUpgradeRequired, spoken, ProtocolVersion, message)
}
