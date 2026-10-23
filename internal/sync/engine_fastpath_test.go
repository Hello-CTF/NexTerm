package sync

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

func deviceCursor(t *testing.T, device *testDevice, userID string) syncCursor {
	t.Helper()
	cursor, err := device.engine.loadCursor(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return cursor
}

func TestIdleRoundsSkipTheFullIDListing(t *testing.T) {
	server := newTestSyncServer(t)
	userID, _ := seedTestSyncUser(t, server.db, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	putDeviceSnippet(t, deviceA, ids.New(), "status", "docker ps", 100)

	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if server.idsFull.Load() != 1 || server.idsUnchanged.Load() != 0 {
		t.Fatalf("first round full=%d unchanged=%d, want one full listing", server.idsFull.Load(), server.idsUnchanged.Load())
	}
	if cursor := deviceCursor(t, deviceA, userID); cursor.ReconciledFor == "" {
		t.Fatalf("cursor after a clean round = %+v, want it marked reconciled", cursor)
	}

	report := syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if server.idsUnchanged.Load() != 1 || report.Pushed != 0 || report.Pulled != 0 {
		t.Fatalf("idle round unchanged=%d report=%+v, want the unchanged fast path and no transfer", server.idsUnchanged.Load(), report)
	}

	// A local edit is still collected and pushed on the fast path.
	putDeviceSnippet(t, deviceA, ids.New(), "logs", "docker logs api", 200)
	if report := syncDevice(t, deviceA, server, "alice", "alice-pw-123"); report.Pushed != 1 {
		t.Fatalf("local edit on the fast path pushed %d, want 1", report.Pushed)
	}
}

func TestAnotherDevicesPushForcesAFullListingAndIsPulled(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	snippetID := ids.New()
	putDeviceSnippet(t, deviceB, snippetID, "from-b", "uptime", 300)
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	full := server.idsFull.Load()
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if server.idsFull.Load() != full+1 {
		t.Fatal("a moved head must list every server ID again")
	}
	snippets, err := deviceA.db.SnippetList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, snippet := range snippets {
		found = found || snippet.ID == snippetID
	}
	if !found {
		t.Fatal("device A did not pull device B's snippet")
	}
}

func TestChangedOptInsInvalidateTheReconciledCursor(t *testing.T) {
	server := newTestSyncServer(t)
	userID, _ := seedTestSyncUser(t, server.db, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")

	cursor := deviceCursor(t, deviceA, userID)
	cursor.ReconciledFor = "optin:00000"
	encoded, err := json.Marshal(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if err := deviceA.db.SettingSet(context.Background(), cursorSettingKey(userID), string(encoded)); err != nil {
		t.Fatal(err)
	}
	full := server.idsFull.Load()
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if server.idsFull.Load() != full+1 {
		t.Fatal("a cursor reconciled under other opt-ins must not take the fast path")
	}
}

func TestPendingObjectsKeepTheNextRoundOnTheFullPath(t *testing.T) {
	report := SyncReport{}
	report.markUnresolvedRef("asset-1")
	if !report.leftPending {
		t.Fatal("an object applied without its dependency must leave the round pending")
	}
}

func TestConflictBackoffGrowsWithJitter(t *testing.T) {
	for attempt := 0; attempt < 3; attempt++ {
		base := time.Duration(100*(attempt+1)) * time.Millisecond
		for i := 0; i < 50; i++ {
			delay := conflictBackoff(attempt)
			if delay < base || delay >= 2*base {
				t.Fatalf("attempt %d delay %v outside [%v, %v)", attempt, delay, base, 2*base)
			}
		}
	}
}
