package durable

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

func TestVersionsRoundTripAndMonotonicFloor(t *testing.T) {
	backend, _ := newUnitBackend(t)
	current := unitRecord(backend, "versions-tab")
	session := newUnitSession(t, backend, current, nil)

	if eventVersion, gridRevision, err := session.Versions(); err != nil || eventVersion != 0 || gridRevision != 0 {
		t.Fatalf("fresh versions = %d/%d, %v; want 0/0, nil", eventVersion, gridRevision, err)
	}
	if err := session.PersistVersions(3, 2); err != nil {
		t.Fatal(err)
	}
	if eventVersion, gridRevision, err := session.Versions(); err != nil || eventVersion != 3 || gridRevision != 2 {
		t.Fatalf("persisted versions = %d/%d, %v; want 3/2, nil", eventVersion, gridRevision, err)
	}
	// The floor never rewinds, even when a racing attachment writes stale values.
	if err := session.PersistVersions(1, 5); err != nil {
		t.Fatal(err)
	}
	if eventVersion, gridRevision, err := session.Versions(); err != nil || eventVersion != 3 || gridRevision != 5 {
		t.Fatalf("rewound versions = %d/%d, %v; want max 3/5, nil", eventVersion, gridRevision, err)
	}
}

func TestVersionsCorruptFileFailsClosed(t *testing.T) {
	backend, _ := newUnitBackend(t)
	current := unitRecord(backend, "corrupt-tab")
	session := newUnitSession(t, backend, current, nil)
	if err := os.WriteFile(backend.versionsPath(current.info.ID), []byte("not-versions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.Versions(); err == nil || !strings.Contains(err.Error(), "parse durable versions") {
		t.Fatalf("Versions error = %v, want parse failure", err)
	}
	if err := session.PersistVersions(1, 1); err == nil {
		t.Fatal("PersistVersions overwrote a corrupt floor")
	}
}

func TestPersistVersionsAfterArtifactsRemovedFails(t *testing.T) {
	backend, _ := newUnitBackend(t)
	current := unitRecord(backend, "removed-tab")
	session := newUnitSession(t, backend, current, nil)
	if err := os.RemoveAll(backend.sessionDir(current.info.ID)); err != nil {
		t.Fatal(err)
	}
	if err := session.PersistVersions(1, 1); err == nil {
		t.Fatal("PersistVersions succeeded without a session directory")
	}
}

func TestVersionsFileIsPrivateAndAtomic(t *testing.T) {
	backend, _ := newUnitBackend(t)
	current := unitRecord(backend, "private-tab")
	session := newUnitSession(t, backend, current, nil)
	if err := session.PersistVersions(7, 4); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(backend.sessionDir(current.info.ID), "versions"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("versions file mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(backend.versionsTempPath(current.info.ID)); !os.IsNotExist(err) {
		t.Fatalf("versions temp file survived commit: %v", err)
	}
}

func TestParseRecordsReadsWindowSize(t *testing.T) {
	backend, runner := newUnitBackend(t)
	current := unitRecord(backend, ids.New())
	current.cols, current.rows = 132, 43
	current.info.Cols, current.info.Rows = 132, 43
	installRecords(backend, runner, current)
	touchUnitSocket(t, backend)
	infos, err := backend.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Cols != 132 || infos[0].Rows != 43 {
		t.Fatalf("List infos = %+v, want 132x43", infos)
	}
}

func TestParseRecordsRejectsMalformedWindowSize(t *testing.T) {
	backend, runner := newUnitBackend(t)
	current := unitRecord(backend, ids.New())
	line := strings.Replace(discoveryLine(backend, current), "|80|24\n", "|0|24\n", 1)
	runner.handler = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "list-panes" {
			return []byte(line), nil
		}
		return nil, nil
	}
	touchUnitSocket(t, backend)
	if _, err := backend.List(context.Background()); err == nil || !strings.Contains(err.Error(), "window width") {
		t.Fatalf("List error = %v, want window width failure", err)
	}
}
