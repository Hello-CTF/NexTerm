package cron

import (
	"context"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type OwnerKind string

const (
	OwnerKindAccount OwnerKind = "account"
	OwnerKindOpen    OwnerKind = "open"
	OwnerKindLegacy  OwnerKind = "legacy"
)

func ownerFromContext(ctx context.Context) (string, OwnerKind) {
	if ownerID := ipc.OwnerID(ctx); ownerID != "" {
		return ownerID, OwnerKindAccount
	}
	return "", OwnerKindOpen
}

func ownerFromModel(row *store.CronJob) (string, OwnerKind) {
	kind := OwnerKind(row.OwnerKind)
	if kind == "" {
		if row.OwnerID != "" {
			kind = OwnerKindAccount
		} else {
			kind = OwnerKindLegacy
		}
	}
	return row.OwnerID, kind
}

func contextWithOwner(ctx context.Context, ownerID string, kind OwnerKind) context.Context {
	return owner.AccountContext(ctx, ownerID)
}

type internalAccessContextKey struct{}

func WithInternalAccess(ctx context.Context) context.Context {
	return context.WithValue(ctx, internalAccessContextKey{}, true)
}

func internalAccess(ctx context.Context) bool {
	internal, _ := ctx.Value(internalAccessContextKey{}).(bool)
	return internal
}

func ownerExecutionError(ownerID string, kind OwnerKind, allowLegacy bool) error {
	switch kind {
	case OwnerKindAccount:
		if ownerID == "" {
			return fmt.Errorf("%w: account owner is empty", ErrInvalidOwner)
		}
	case OwnerKindOpen:
		if ownerID != "" {
			return fmt.Errorf("%w: open owner ID is not empty", ErrInvalidOwner)
		}
	case OwnerKindLegacy:
		if ownerID != "" {
			return fmt.Errorf("%w: legacy owner ID is not empty", ErrInvalidOwner)
		}
		if !allowLegacy {
			return ErrOwnerlessLegacy
		}
	default:
		return fmt.Errorf("%w: kind %q", ErrInvalidOwner, kind)
	}
	return nil
}
