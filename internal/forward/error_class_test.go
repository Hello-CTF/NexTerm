package forward

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

// A-04: forward commands must classify session lifecycle errors instead of
// leaking generic internal errors — disconnected/redialing sessions surface as
// CodeDisconnected, identity-less callers without an open deployment as
// CodeForbidden, and identity-less open-access callers keep the session-state
// classification.

func requireErrorCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func TestForwardClassifiesDisconnectedSessionAcrossCommands(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	rule := fixture.saveRule(t, freeSavedRulePort(t))
	if _, err := fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.Disconnect(fixture.connected.ID); err != nil {
		t.Fatal(err)
	}

	_, err := fixture.service.CreateLocal(fixture.ctx, CreateLocalArgs{
		SessionID: fixture.connected.ID, TargetHost: "remote-only.invalid", TargetPort: 4567,
	})
	requireErrorCode(t, err, ipc.CodeDisconnected)

	_, err = fixture.service.CreateSocks(fixture.ctx, CreateSocksArgs{SessionID: fixture.connected.ID, AcknowledgeRisk: true})
	requireErrorCode(t, err, ipc.CodeDisconnected)

	_, err = fixture.service.CreateRemote(fixture.ctx, CreateRemoteArgs{
		SessionID: fixture.connected.ID, TargetHost: "127.0.0.1", TargetPort: 8080,
	})
	requireErrorCode(t, err, ipc.CodeDisconnected)

	_, err = fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID)
	requireErrorCode(t, err, ipc.CodeDisconnected)

	_, err = fixture.service.RetryFor(fixture.ctx, rule.ID, fixture.connected.ID)
	requireErrorCode(t, err, ipc.CodeDisconnected)

	if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 1 {
		t.Fatalf("disconnected session changed runtimes: %+v", runtimes)
	}
	if _, err := fixture.db.ForwardRuleGet(fixture.ctx, rule.ID); err != nil {
		t.Fatalf("disconnected session removed the rule definition: %v", err)
	}
}

func TestForwardClassifiesIdentityLessWithoutOpenAccessAsForbidden(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	identityLess := context.Background()

	_, err := fixture.service.CreateLocal(identityLess, CreateLocalArgs{
		SessionID: fixture.connected.ID, TargetHost: "remote-only.invalid", TargetPort: 4567,
	})
	requireErrorCode(t, err, ipc.CodeForbidden)

	_, err = fixture.service.CreateSocks(identityLess, CreateSocksArgs{SessionID: fixture.connected.ID, AcknowledgeRisk: true})
	requireErrorCode(t, err, ipc.CodeForbidden)

	open := session.WithOpenAccess(identityLess)
	spec, err := fixture.service.CreateLocal(open, CreateLocalArgs{
		SessionID: fixture.connected.ID, TargetHost: "remote-only.invalid", TargetPort: 4567,
	})
	if err != nil {
		t.Fatalf("open-access identity-less create: %v", err)
	}
	if spec.SessionID != fixture.connected.ID {
		t.Fatalf("open-access spec = %+v", spec)
	}
}

func TestForwardClassifiesIdentityLessOpenAccessBySessionState(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	if err := fixture.manager.Disconnect(fixture.connected.ID); err != nil {
		t.Fatal(err)
	}
	open := session.WithOpenAccess(context.Background())

	_, err := fixture.service.CreateLocal(open, CreateLocalArgs{
		SessionID: fixture.connected.ID, TargetHost: "remote-only.invalid", TargetPort: 4567,
	})
	requireErrorCode(t, err, ipc.CodeDisconnected)

	_, err = fixture.service.CreateSocks(open, CreateSocksArgs{SessionID: fixture.connected.ID, AcknowledgeRisk: true})
	requireErrorCode(t, err, ipc.CodeDisconnected)
}

func TestForwardClassifiesMissingSessionAsNotFound(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	_, err := fixture.service.CreateLocal(fixture.ctx, CreateLocalArgs{
		SessionID: "missing-session", TargetHost: "remote-only.invalid", TargetPort: 4567,
	})
	requireErrorCode(t, err, ipc.CodeNotFound)

	_, err = fixture.service.CreateSocks(fixture.ctx, CreateSocksArgs{SessionID: "missing-session", AcknowledgeRisk: true})
	requireErrorCode(t, err, ipc.CodeNotFound)
}
