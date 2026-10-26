package base

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrMetadataConflict = errors.New("durable terminal metadata changed")

type DurableCreateOptions struct {
	ID      string
	Command []string
	Dir     string
	Env     []string
	Cols    uint32
	Rows    uint32
}

type DurableDescriptor struct {
	ID          string
	Incarnation string
	// Generation is the helper generation that listed the session.
	Generation       string
	CreatedAt        time.Time
	Title            string
	MetadataRevision uint64
	Cols             uint32
	Rows             uint32
	Dead             bool
	ExitCode         *int
	Signal           string
	FinishedAt       *time.Time
}

type DurableIdentity struct {
	CreatedAt   time.Time
	Incarnation string
	// Generation is the helper generation the session was last seen in.
	// When set, attach fails with durable.ErrGenerationMismatch after a
	// helper restart; an empty value skips that check.
	Generation string
}

type DurableRenameOptions struct {
	ID                       string
	Incarnation              string
	RequestID                string
	ExpectedMetadataRevision uint64
	Title                    string
}

type DurableRenameResult struct {
	Title            string
	MetadataRevision uint64
}

type DurableMetadataConflictError struct {
	CurrentTitle            string
	CurrentMetadataRevision uint64
}

func (e *DurableMetadataConflictError) Error() string {
	return fmt.Sprintf("%s: current title %q at revision %d", ErrMetadataConflict, e.CurrentTitle, e.CurrentMetadataRevision)
}

func (e *DurableMetadataConflictError) Unwrap() error {
	return ErrMetadataConflict
}

type DurableDescriptorLister interface {
	ListDurableDescriptors(context.Context) ([]DurableDescriptor, error)
}

type DurableMetadataProvider interface {
	DurableMetadataSupported(context.Context) (bool, error)
	RenameDurable(context.Context, DurableRenameOptions) (DurableRenameResult, error)
}

// DurableIdentityAttacher attaches only to the durable session whose identity
// matches; there is no unchecked attach path.
type DurableIdentityAttacher interface {
	AttachWithIdentity(context.Context, string, DurableIdentity) (DurableAttachment, error)
	AttachFromWithIdentity(context.Context, string, DurableIdentity, uint64) (DurableAttachment, error)
}

type DurableIdentityKiller interface {
	KillWithIdentity(context.Context, string, DurableIdentity) error
}

type DurableProvider interface {
	Create(context.Context, DurableCreateOptions) (DurableAttachment, error)
	DurableIdentityAttacher
}

type DurableAttachment interface {
	Channel
	Kill(context.Context) error
}
