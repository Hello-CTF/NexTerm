package sync

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// Every saved link change, including a switch to another account, is reported
// so a desktop exposing its terminals can stop at once.
func TestLinkSetReportsLinkChange(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	fixture.createUser(t, "bob", "bob-pw-123")
	instance := newTestInstance(t, true)
	var changes atomic.Int32
	instance.service.SetLinkChanged(func() { changes.Add(1) })

	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
		t.Fatal(err)
	}
	afterFirst := changes.Load()
	if afterFirst == 0 {
		t.Fatal("saving the link was not reported")
	}
	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "bob", "bob-pw-123")); err != nil {
		t.Fatal(err)
	}
	if changes.Load() <= afterFirst {
		t.Fatal("switching the link to another account was not reported")
	}
	link, err := instance.service.LinkGet(context.Background())
	if err != nil || link.Username != "bob" {
		t.Fatalf("link = %+v %v, want bob", link, err)
	}
}

// Logging out clears the credentials and still reports the link change.
func TestLinkSetLogoutReportsLinkChange(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
		t.Fatal(err)
	}
	var changes atomic.Int32
	instance.service.SetLinkChanged(func() { changes.Add(1) })

	if _, err := instance.service.LinkSet(context.Background(), LinkPatch{Username: testPtr(""), Password: testPtr("")}); err != nil {
		t.Fatal(err)
	}
	if changes.Load() == 0 {
		t.Fatal("logout was not reported")
	}
}

// A probe records its outcome only; it does not wake link listeners.
func TestRecordProbeDoesNotReportLinkChange(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	ctx := context.Background()
	if _, err := instance.service.LinkSet(ctx, linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
		t.Fatal(err)
	}
	var changes atomic.Int32
	instance.service.SetLinkChanged(func() { changes.Add(1) })

	instance.service.recordProbe(ctx, errors.New("probe failed"))
	link, err := instance.service.LinkGet(ctx)
	if err != nil || link.LastError == "" {
		t.Fatalf("probe result not recorded: %+v %v", link, err)
	}
	instance.service.recordProbe(ctx, nil)
	link, err = instance.service.LinkGet(ctx)
	if err != nil || link.LastError != "" || link.VerifiedAt == 0 {
		t.Fatalf("probe success not recorded: %+v %v", link, err)
	}
	if changes.Load() != 0 {
		t.Fatalf("probe reported %d link changes, want none", changes.Load())
	}
}
