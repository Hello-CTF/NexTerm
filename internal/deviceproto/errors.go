package deviceproto

import (
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

var (
	ErrProtocol = errors.New("device protocol error")
	ErrInvalid  = errors.New("device request rejected")
)

const (
	CodeInternal    = "internal"
	CodeNotFound    = "not_found"
	CodePermission  = "permission"
	CodeExists      = "exists"
	CodeInvalid     = "invalid_input"
	CodeProtocol    = "protocol"
	CodeUnsupported = "unsupported"
	CodeNoMedia     = "no_media"
)

// ErrorCode maps a server-side failure to its wire code, preserving the
// os/fs error semantics the client maps back from.
func ErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrProtocol):
		return CodeProtocol
	case errors.Is(err, ErrInvalid):
		return CodeInvalid
	case errors.Is(err, base.ErrUnsupported):
		return CodeUnsupported
	case errors.Is(err, base.ErrNoMedia):
		return CodeNoMedia
	case errors.Is(err, fs.ErrNotExist):
		return CodeNotFound
	case errors.Is(err, fs.ErrPermission):
		return CodePermission
	case errors.Is(err, fs.ErrExist):
		return CodeExists
	default:
		return CodeInternal
	}
}

// CodedError rebuilds the client-side error for a wire code, so
// errors.Is(err, fs.ErrNotExist) and friends keep working across the bridge.
func CodedError(code, message string) error {
	return fmt.Errorf("%w: %s", errorSentinel(code), message)
}

func errorSentinel(code string) error {
	switch code {
	case CodeNotFound:
		return fs.ErrNotExist
	case CodePermission:
		return fs.ErrPermission
	case CodeExists:
		return fs.ErrExist
	case CodeInvalid:
		return ErrInvalid
	case CodeProtocol:
		return ErrProtocol
	case CodeUnsupported:
		return base.ErrUnsupported
	case CodeNoMedia:
		return base.ErrNoMedia
	default:
		return fmt.Errorf("%w (%s)", ErrProtocol, code)
	}
}

// WriteError reports an operation failure to the client; the returned error
// is nil once the error frame is on the wire.
func WriteError(writer io.Writer, err error) error {
	return WriteMessage(writer, FrameError, ErrorMsg{Code: ErrorCode(err), Message: err.Error()})
}

// ReadError unwraps a FrameError payload.
func ReadError(payload []byte) error {
	var msg ErrorMsg
	if err := UnmarshalPayload(payload, &msg); err != nil {
		return err
	}
	return CodedError(msg.Code, msg.Message)
}
