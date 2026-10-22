package supervisor

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const ProtocolVersion = 2

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
)

const (
	// featureExec is advertised in the hello handshake by peers that support
	// non-PTY exec sessions (createMsg.Pty == false and frameExecOutput).
	featureExec            = "exec"
	featureMetadata        = "metadata"
	featureReplayGzip      = "replay_gzip"
	featureRuntimeMetadata = "runtime_metadata"
	featureActivity        = "activity"
)

// Exec output stream discriminators, the first byte of a frameExecOutput
// payload.
const (
	streamStdout byte = 1
	streamStderr byte = 2
)

type helloMsg struct {
	Version     int               `json:"version"`
	StateDigest string            `json:"state_digest,omitempty"`
	Features    []string          `json:"features,omitempty"`
	Runtime     *RuntimeMetadata  `json:"runtime,omitempty"`
	Activity    *ActivitySnapshot `json:"activity,omitempty"`
}

type createMsg struct {
	ID      string   `json:"id,omitempty"`
	Attempt string   `json:"attempt,omitempty"`
	Command []string `json:"command,omitempty"`
	Dir     string   `json:"dir,omitempty"`
	Env     []string `json:"env,omitempty"`
	Cols    uint32   `json:"cols,omitempty"`
	Rows    uint32   `json:"rows,omitempty"`
	// Pty is nil or true for a PTY session (the only mode pre-feature peers
	// know); false requests a one-shot non-PTY exec session.
	Pty *bool `json:"pty,omitempty"`
}

type attachMsg struct {
	ID                string `json:"id"`
	ExpectCreatedAt   int64  `json:"expect_created_at_unix_nano,omitempty"`
	ExpectIncarnation string `json:"expect_incarnation,omitempty"`
	// Offset is the absolute byte offset of the recording the client has consumed; the server seeks there
	// and resumes. Old servers ignore this field and replay from 0; the client degrades by skipping the prefix in-stream.
	Offset uint64 `json:"offset,omitempty"`
}

type killSessionMsg struct {
	ID                string `json:"id"`
	Attempt           string `json:"attempt,omitempty"`
	ExpectCreatedAt   int64  `json:"expect_created_at_unix_nano,omitempty"`
	ExpectIncarnation string `json:"expect_incarnation,omitempty"`
}

type resizeMsg struct {
	Cols uint32 `json:"cols"`
	Rows uint32 `json:"rows"`
}

type versionsMsg struct {
	Event uint64 `json:"event"`
	Grid  uint64 `json:"grid"`
}

type inputAckMsg struct {
	Written int `json:"written"`
}

type errorMsg struct {
	Code             string `json:"code"`
	Message          string `json:"message"`
	ID               string `json:"id,omitempty"`
	Title            string `json:"title,omitempty"`
	MetadataRevision uint64 `json:"metadata_revision,omitempty"`
}

type exitMsg struct {
	Code   *int   `json:"code,omitempty"`
	Signal string `json:"signal,omitempty"`
}

const (
	codeInternal         = "internal"
	codeNotFound         = "not_found"
	codeAlreadyExists    = "already_exists"
	codeIdentity         = "identity"
	codeIdentityChanged  = "identity_changed"
	codeMetadataConflict = "metadata_conflict"
	codeExited           = "exited"
	codeInvalidInput     = "invalid_input"
	codeUnavailable      = "unavailable"
	codeClosed           = "closed"
	codeProtocol         = "protocol"
	codeVersionMismatch  = "version_mismatch"
	codeStateMismatch    = "state_mismatch"
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
	case codeIdentity, codeIdentityChanged:
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
