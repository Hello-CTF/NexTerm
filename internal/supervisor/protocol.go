package supervisor

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const ProtocolVersion = 1

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
)

type helloMsg struct {
	Version int `json:"version"`
}

type createMsg struct {
	ID      string   `json:"id,omitempty"`
	Command []string `json:"command,omitempty"`
	Dir     string   `json:"dir,omitempty"`
	Env     []string `json:"env,omitempty"`
	Cols    uint32   `json:"cols,omitempty"`
	Rows    uint32   `json:"rows,omitempty"`
}

type attachMsg struct {
	ID                string `json:"id"`
	ExpectCreatedAt   int64  `json:"expect_created_at_unix_nano,omitempty"`
	ExpectIncarnation string `json:"expect_incarnation,omitempty"`
}

type killSessionMsg struct {
	ID                string `json:"id"`
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
	Code    string `json:"code"`
	Message string `json:"message"`
}

type exitMsg struct {
	Code   *int   `json:"code,omitempty"`
	Signal string `json:"signal,omitempty"`
}

const (
	codeInternal        = "internal"
	codeNotFound        = "not_found"
	codeAlreadyExists   = "already_exists"
	codeIdentity        = "identity"
	codeExited          = "exited"
	codeInvalidInput    = "invalid_input"
	codeUnavailable     = "unavailable"
	codeClosed          = "closed"
	codeProtocol        = "protocol"
	codeVersionMismatch = "version_mismatch"
)

func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return codeNotFound
	case errors.Is(err, ErrAlreadyExists):
		return codeAlreadyExists
	case errors.Is(err, ErrIdentity):
		return codeIdentity
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

func codedError(code, message string) error {
	sentinel := errorSentinel(code)
	if message == "" {
		return &wireError{sentinel}
	}
	return &wireError{fmt.Errorf("%w: %s", sentinel, message)}
}

func errorSentinel(code string) error {
	switch code {
	case codeNotFound:
		return ErrNotFound
	case codeAlreadyExists:
		return ErrAlreadyExists
	case codeIdentity:
		return ErrIdentity
	case codeExited:
		return ErrExited
	case codeInvalidInput:
		return ErrInvalidInput
	case codeUnavailable:
		return ErrUnavailable
	case codeClosed:
		return ErrClosed
	case codeProtocol, codeVersionMismatch:
		return ErrProtocol
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
