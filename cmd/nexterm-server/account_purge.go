package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/forward"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/server"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

// accountSessions is the slice of the session manager an account deletion needs.
type accountSessions interface {
	ListSessionsFor(ctx context.Context) ([]session.SessionInfo, error)
	KillTabFor(ctx context.Context, id string) error
	DisconnectFor(ctx context.Context, id string) error
}

// accountForwards is the slice of the forward service an account deletion needs.
type accountForwards interface {
	ListFor(ctx context.Context) []forward.Spec
	RemoveFor(ctx context.Context, id string) error
}

// accountPurgeFor stops the live state a deleted account owns on this server: its port forwards, then every
// terminal tab (durable remote terminals included, since nobody can reattach to them) and every session.
// Each session is disconnected twice: the first call ends the connection, the second reaps the now
// tab-less record so the manager keeps nothing for the account.
func accountPurgeFor(sessions accountSessions, forwards accountForwards) server.AccountPurgeFunc {
	return func(ctx context.Context, ownerID string) error {
		if ownerID == "" {
			return nil
		}
		ownerCtx := ipc.WithUserID(context.WithoutCancel(ctx), ownerID)
		var errs []error
		if forwards != nil {
			for _, spec := range forwards.ListFor(ownerCtx) {
				if err := forwards.RemoveFor(ownerCtx, spec.ID); err != nil {
					errs = append(errs, fmt.Errorf("remove forward %s: %w", spec.ID, err))
				}
			}
		}
		if sessions == nil {
			return errors.Join(errs...)
		}
		infos, err := sessions.ListSessionsFor(ownerCtx)
		if err != nil {
			return errors.Join(append(errs, fmt.Errorf("list sessions: %w", err))...)
		}
		for _, info := range infos {
			for _, tabID := range info.Tabs {
				if err := sessions.KillTabFor(ownerCtx, tabID); err != nil && !errors.Is(err, session.ErrTabNotFound) && !errors.Is(err, session.ErrSessionNotFound) {
					errs = append(errs, fmt.Errorf("close terminal %s: %w", tabID, err))
				}
			}
			for range 2 {
				if err := sessions.DisconnectFor(ownerCtx, info.ID); err != nil {
					if !errors.Is(err, session.ErrSessionNotFound) {
						errs = append(errs, fmt.Errorf("disconnect session %s: %w", info.ID, err))
					}
					break
				}
			}
		}
		return errors.Join(errs...)
	}
}

// accountForwardsFor avoids a typed-nil interface when the forward service is absent.
func accountForwardsFor(service *forward.Service) accountForwards {
	if service == nil {
		return nil
	}
	return service
}
