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
	ID               string
	Incarnation      string
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

type DurableIdentityAttacher interface {
	AttachWithIdentity(context.Context, string, DurableIdentity) (DurableAttachment, error)
	AttachFromWithIdentity(context.Context, string, DurableIdentity, uint64) (DurableAttachment, error)
}

type DurableIdentityKiller interface {
	KillWithIdentity(context.Context, string, DurableIdentity) error
}

type DurableProvider interface {
	Create(context.Context, DurableCreateOptions) (DurableAttachment, error)
	Attach(context.Context, string) (DurableAttachment, error)
}

type DurableAttachment interface {
	Channel
	Kill(context.Context) error
}
