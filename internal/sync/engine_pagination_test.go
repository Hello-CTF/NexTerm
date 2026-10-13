package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// When the remote new revisions of two large snippets span two pages, the engine must drain pagination and complete LWW before pushing,
// never overwriting unpulled pages of new data with stale local objects; idle sync pushes nothing after convergence.
func TestEngineDrainsPaginationBeforePush(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	bigBody := strings.Repeat("x", 50<<20)
	snippet1 := ids.New()
	snippet2 := ids.New()
	putDeviceSnippet(t, deviceA, snippet1, "大片段一", bigBody, 100)
	putDeviceSnippet(t, deviceA, snippet2, "大片段二", bigBody, 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// B updates both objects to a higher revision and pushes; the combined wire cost of the two ciphertexts exceeds the single-page budget.
	future := ids.NowMS() + 3600_000
	putDeviceSnippet(t, deviceB, snippet1, "大片段一-新版", "new-"+bigBody, future)
	putDeviceSnippet(t, deviceB, snippet2, "大片段二-新版", "new-"+bigBody, future)
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// A syncs: it must drain both pages and complete LWW, never overwriting the remote new revision with stale local objects.
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	snippetRows, err := deviceA.db.SnippetList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string]string{}
	for _, row := range snippetRows {
		contents[row.Name] = row.Body
	}
	if contents["大片段一-新版"] != "new-"+bigBody || contents["大片段二-新版"] != "new-"+bigBody {
		t.Fatal("deviceA must keep the remote new revisions of both large snippets")
	}
	if len(contents) != 2 {
		t.Fatalf("deviceA snippets=%v, want exactly the two new revisions", contents)
	}

	// After convergence, idle sync pushes nothing.
	reportA := syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if reportA.Pulled+reportA.Applied+reportA.Pushed != 0 {
		t.Fatalf("deviceA idle sync not quiescent: %+v", reportA)
	}
	reportB := syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if reportB.Pulled+reportB.Applied+reportB.Pushed != 0 {
		t.Fatalf("deviceB idle sync not quiescent: %+v", reportB)
	}
}
