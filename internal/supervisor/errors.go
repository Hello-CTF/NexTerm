package supervisor

import (
	"errors"
	"fmt"
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
