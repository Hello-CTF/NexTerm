package sync

import (
	"context"
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
