package sync

import (
	"context"
	"slices"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// An object of a kind this build does not know (written by a newer peer under the
// same DEK) is skipped without being overwritten, and it is read again once an
// upgraded build knows the kind instead of staying quarantined forever.
func TestEngineUnknownKindObjectIsRetriedAfterUpgrade(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	device := newTestDevice(t)
	ctx := context.Background()

	syncDevice(t, device, server, "alice", "alice-pw-123")
	session, err := device.engine.ensureSession(ctx, device.config(server, "alice", "alice-pw-123"))
	if err != nil {
		t.Fatal(err)
	}
	const futureKind = "future-kind-v99"
	objectID := ids.New()
	blob, err := sealObject(mustDEK(t, server, "alice", "alice-pw-123"), []byte(`{"x":1}`), objectID, futureKind)
	if err != nil {
		t.Fatal(err)
	}
	idsResponse, err := session.client.ids(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.client.push(ctx, idsResponse.Head, []WireObject{{ID: objectID, Blob: blob}}); err != nil {
		t.Fatal(err)
	}

	report := syncDevice(t, device, server, "alice", "alice-pw-123")
	if report.DecryptFailed == 0 || report.Pushed != 0 {
		t.Fatalf("unknown kind must be skipped without a push: %+v", report)
	}

	previous := objectKinds
	objectKinds = append(slices.Clone(previous), futureKind)
	t.Cleanup(func() { objectKinds = previous })

	report = syncDevice(t, device, server, "alice", "alice-pw-123")
	if report.Pulled == 0 || report.DecryptFailed != 0 {
		t.Fatalf("object of a newly known kind was not read again after upgrade: %+v", report)
	}
}
