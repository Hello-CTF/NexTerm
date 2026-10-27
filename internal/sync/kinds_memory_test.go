package sync

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/memory"
	"github.com/Hello-CTF/NexTerm/internal/ids"
)

var localMemoryScope = memory.Scope{Tenant: "local", Subject: "default"}

func deviceMemory(t *testing.T, device *testDevice) *memory.Store {
	t.Helper()
	entries, err := memory.NewStore(device.db.GORM())
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// AI memory stays local until the user opts in; then entries and deletions reach the other device.
func TestMemorySyncIsOptInAndCarriesDeletions(t *testing.T) {
	ctx := context.Background()
	server := newTestSyncServer(t)
	userID, _ := seedTestSyncUser(t, server.db, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	entry, err := deviceMemory(t, deviceA).Create(ctx, localMemoryScope, memory.CreateInput{Topic: "deploy", Content: "api runs on port 8080"})
	if err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, err := deviceMemory(t, deviceB).Get(ctx, localMemoryScope, entry.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("memory synced while the switch is off: %v", err)
	}

	for _, device := range []*testDevice{deviceA, deviceB} {
		if err := device.db.SetMemorySyncOptIn(ctx, userID, true); err != nil {
			t.Fatal(err)
		}
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	got, err := deviceMemory(t, deviceB).Get(ctx, localMemoryScope, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Topic != "deploy" || got.Content != entry.Content || got.UpdatedAt != entry.UpdatedAt {
		t.Fatalf("received entry=%+v", got)
	}

	if err := deviceMemory(t, deviceA).Delete(ctx, localMemoryScope, entry.ID, entry.Version); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, err := deviceMemory(t, deviceB).Get(ctx, localMemoryScope, entry.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("deleted entry still on the other device: %v", err)
	}
}

// Incoming memory is redacted with this device's rules before it is stored.
func TestApplyObjectsRedactsIncomingMemory(t *testing.T) {
	instance := newTestInstance(t, false)
	id := ids.New()
	result := mustApplyObjects(t, instance, ApplyObject{ID: id, Kind: KindMemory, Payload: applyPayload(t, memoryObject{
		ID: id, Topic: "db", Content: "connect with password=hunter2", CreatedAt: 100, UpdatedAt: 100,
	})})
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	entries, err := memory.NewStore(instance.db.GORM())
	if err != nil {
		t.Fatal(err)
	}
	got, err := entries.Get(context.Background(), localMemoryScope, id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Content, "hunter2") || !got.Redacted {
		t.Fatalf("stored entry=%+v, want the secret redacted", got)
	}
}
