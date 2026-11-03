package supervisor

import (
	"errors"
	"fmt"
	"path/filepath"
)

var (
	ErrUnavailable   = errors.New("supervisor session unavailable")
	ErrNotFound      = errors.New("supervisor session not found")
	ErrAlreadyExists = errors.New("supervisor session already exists")
	ErrIdentity      = errors.New("supervisor session identity changed")
	ErrExited        = errors.New("supervisor session process exited")
	ErrClosed        = errors.New("supervisor session attachment closed")
	ErrInvalidInput  = errors.New("invalid supervisor session input")
	ErrProtocol      = errors.New("supervisor protocol violation")
	ErrStateMismatch = errors.New("supervisor state mismatch")
	ErrUnsupported   = errors.New("supervisor capability unsupported")
	// ErrStateSeenUnsupported reports that seen attention states cannot be shared
	// through this helper because its session has state detection disabled.
	// Unlike ErrUnavailable it is permanent.
	ErrStateSeenUnsupported = errors.New("supervisor does not share seen attention states")
	ErrActiveSessions       = errors.New("supervisor has active sessions")
	// ErrLimit reports a create the helper refused because a configured
	// session-count or create-rate limit was reached.
	ErrLimit = errors.New("supervisor limit reached")
	// ErrGenerationMismatch reports a request that expected another helper
	// generation: the helper restarted since the client last saw it.
	ErrGenerationMismatch = errors.New("supervisor helper generation changed")

	ErrVersionMismatch = fmt.Errorf("%w: protocol version mismatch", ErrProtocol)
	// ErrHelperUpgradeRequired reports a helper that speaks an older protocol
	// than this client; ErrClientUpgradeRequired the reverse. Both are version
	// mismatches, so callers that refuse to replace an incompatible helper keep
	// doing so.
	ErrHelperUpgradeRequired = fmt.Errorf("%w: supervisor helper needs upgrade", ErrVersionMismatch)
	ErrClientUpgradeRequired = fmt.Errorf("%w: client needs upgrade", ErrVersionMismatch)
	// ErrLegacyHelperRunning is the helper upgrade case where this side is
	// current but an older helper still owns the state directory, as opposed
	// to an older helper or agent answering the hello itself.
	ErrLegacyHelperRunning = fmt.Errorf("%w: an older helper still owns the state directory", ErrHelperUpgradeRequired)
)

// ErrUntrustedEndpoint reports a helper endpoint or its directory that belongs
// to another user, or whose owner cannot be confirmed. It does not recover on
// its own and needs the user to act, so it is deliberately not an ErrProtocol.
var ErrUntrustedEndpoint = errors.New("supervisor helper endpoint is not trusted")

// EndpointKind says which part of a helper endpoint was refused.
type EndpointKind int

const (
	// EndpointDir is the private directory holding the helper socket.
	EndpointDir EndpointKind = iota
	// EndpointSocket is a unix socket served by another user.
	EndpointSocket
	// EndpointPipe is a Windows named pipe served by another user.
	EndpointPipe
	// EndpointElevated is a Windows helper whose process cannot be inspected
	// because it runs with higher privileges.
	EndpointElevated
)

// UntrustedEndpointError is the user-facing form of ErrUntrustedEndpoint.
type UntrustedEndpointError struct {
	Kind EndpointKind
	// Path is the directory, socket path or pipe name that was refused.
	Path string
	// OwnerUID is the foreign owner on unix, or -1 when unknown.
	OwnerUID int
	// Elevated is set when Windows denied access to the helper process.
	Elevated bool
}

func (e *UntrustedEndpointError) Error() string {
	switch e.Kind {
	case EndpointSocket:
		return fmt.Sprintf("持久终端组件端点 %s 由其他用户%s提供，已拒绝连接。请删除目录 '%s' 后重试（可能需要管理员权限）。", e.Path, e.owner(), filepath.Dir(e.Path))
	case EndpointPipe:
		return fmt.Sprintf("持久终端组件管道 %s 由其他用户提供，已拒绝连接。请注销或重启后重试。", e.Path)
	case EndpointElevated:
		return "持久终端组件以管理员权限运行，请以相同权限启动本程序，或重启持久终端组件。"
	default:
		return fmt.Sprintf("持久终端组件目录 %s 被其他用户%s占用，已拒绝使用。请确认没有其他账户在使用它，然后删除该目录后重试，例如：sudo rm -r '%s'", e.Path, e.owner(), e.Path)
	}
}

func (e *UntrustedEndpointError) owner() string {
	if e.OwnerUID < 0 {
		return ""
	}
	return fmt.Sprintf("（uid %d）", e.OwnerUID)
}

func (e *UntrustedEndpointError) Unwrap() error { return ErrUntrustedEndpoint }

// Is keeps the directory case an ErrInvalidInput for callers that already
// handled the earlier owner check.
func (e *UntrustedEndpointError) Is(target error) bool {
	return e.Kind == EndpointDir && target == ErrInvalidInput
}
