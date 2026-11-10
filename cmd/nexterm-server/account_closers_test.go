//go:build darwin || linux

package main

import (
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/db"
	fleetserver "github.com/Hello-CTF/NexTerm/internal/fleet/server"
)

type userCloseProbe struct{ user, except string }

func (p *userCloseProbe) CloseAccountSession(string) error { return nil }
func (p *userCloseProbe) CloseAccountUser(user, except string) {
	p.user, p.except = user, except
}

type sessionOnlyCloser struct{}

func (sessionOnlyCloser) CloseAccountSession(string) error { return nil }

// The database service takes part in user-level revocation like the remote desktop service.
var _ interface{ CloseAccountUser(string, string) } = (*db.Service)(nil)

// Owner device bridge relays close with the login session that opened them.
var _ interface {
	CloseAccountSession(string) error
	CloseAccountUser(string, string)
} = (*fleetserver.Service)(nil)

func TestAccountConnectionClosersFanOutUserRevocation(t *testing.T) {
	probe := &userCloseProbe{}
	closers := accountConnectionClosers{sessionOnlyCloser{}, probe}
	closers.CloseAccountUser("u1", "current")
	if probe.user != "u1" || probe.except != "current" {
		t.Fatalf("probe = %+v", probe)
	}
}
