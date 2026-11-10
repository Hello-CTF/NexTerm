//go:build !windows

package installer

import (
	"strings"
	"testing"
)

const (
	dbBeforeUpgrade  = "sqlite-before-upgrade\n"
	walBeforeUpgrade = "wal-before-upgrade\n"
)

// upgradeWithDatabase installs v1, seeds a SQLite database written by it and
// upgrades to v2 with nexterm-server active.
func upgradeWithDatabase(t *testing.T) (*sandbox, string, runResult) {
	t.Helper()
	s := newSandbox(t, stubOptions{})
	s.seedFirstInstall()
	s.write("var/lib/nexterm/data.db", dbBeforeUpgrade)
	s.write("var/lib/nexterm/data.db-wal", walBeforeUpgrade)
	s.removeLog("systemctl.log")
	v2 := s.payload("v2")
	got := s.mustInstall(v2, installOptions{active: "nexterm-server.service"})
	return s, v2, got
}

func TestSystemdUpgradeSnapshotsSQLiteWhileStopped(t *testing.T) {
	s, _, got := upgradeWithDatabase(t)
	expect(t, s.read("opt/nexterm/.rollback/db/nexterm/data.db") == dbBeforeUpgrade, "data.db not snapshotted")
	expect(t, s.read("opt/nexterm/.rollback/db/nexterm/data.db-wal") == walBeforeUpgrade, "WAL not snapshotted with data.db")
	expect(t, s.exists("opt/nexterm/.rollback/db/complete"), "snapshot not marked complete")
	expect(t, !s.exists("opt/nexterm/.rollback/db/onlyserver"), "a database shared by both units must be snapshotted once")
	expect(t, s.mode("opt/nexterm/.rollback") == 0o700, "rollback dir mode %o", s.mode("opt/nexterm/.rollback"))
	calls := s.systemctlCalls()
	stop, restart := -1, -1
	for i, call := range calls {
		switch call {
		case "stop nexterm-server.service":
			stop = i
		case "restart nexterm-server.service":
			restart = i
		}
	}
	expect(t, stop >= 0 && restart > stop, "the unit must be stopped for the snapshot and started afterwards: %v", calls)
	expect(t, strings.Contains(got.stdout, "saved database"), "stdout: %s", got.stdout)
}

func TestSystemdUpgradeRetryKeepsFirstDatabaseSnapshot(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.seedFirstInstall()
	s.write("var/lib/nexterm/data.db", dbBeforeUpgrade)
	v2 := s.payload("v2")
	s.mustFail(v2, installOptions{failAt: "bootstrap", active: "nexterm-server.service"})
	expect(t, s.read("opt/nexterm/.rollback/db/nexterm/data.db") == dbBeforeUpgrade, "snapshot missing after the failed attempt")
	s.write("var/lib/nexterm/data.db", "written-between-attempts\n")
	retry := s.mustInstall(v2, installOptions{})
	expect(t, strings.Contains(retry.stdout, "preserving the database snapshot"), "stdout: %s", retry.stdout)
	expect(t, s.read("opt/nexterm/.rollback/db/nexterm/data.db") == dbBeforeUpgrade, "retry must keep the original snapshot")
}

func TestSystemdUpgradeSkipsRemoteDatabaseSnapshot(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.seedFirstInstall()
	for _, env := range envFiles {
		s.write("etc/nexterm/"+env, "NEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_MASTER_KEY_FILE=/etc/nexterm/master.key\nNEXTERM_DB=postgres\nNEXTERM_DB_DSN=postgres://nexterm@db.example/nexterm\n")
	}
	s.write("var/lib/nexterm/data.db", dbBeforeUpgrade)
	s.removeLog("systemctl.log")
	got := s.mustInstall(s.payload("v2"), installOptions{active: "nexterm-server.service"})
	expect(t, strings.Contains(got.stdout, "uses a remote database"), "stdout: %s", got.stdout)
	expect(t, !s.exists("opt/nexterm/.rollback/db/nexterm"), "a remote backend must not snapshot the unused SQLite file")
	expect(t, !contains(s.systemctlCalls(), "stop nexterm-server.service"), "nothing to snapshot, so no stop: %v", s.systemctlCalls())
}

func TestSystemdRollbackRestoresDatabaseOnlyWhenConfirmed(t *testing.T) {
	s, v2, _ := upgradeWithDatabase(t)
	s.write("var/lib/nexterm/data.db", "migrated-by-v2\n")
	s.write("var/lib/nexterm/data.db-shm", "shm-by-v2\n")
	binaryAfterUpgrade := s.read("opt/nexterm/nexterm-server")

	refused := s.mustFail(v2, installOptions{args: []string{"--rollback"}})
	expect(t, strings.Contains(refused.stderr, "--yes") && strings.Contains(refused.stdout, "is lost"), "stdout: %s stderr: %s", refused.stdout, refused.stderr)
	expect(t, s.read("opt/nexterm/nexterm-server") == binaryAfterUpgrade, "a refused rollback must not change the payload")
	expect(t, s.read("var/lib/nexterm/data.db") == "migrated-by-v2\n", "a refused rollback must not change the database")

	s.removeLog("systemctl.log")
	s.mustInstall(v2, installOptions{args: []string{"--rollback", "--yes"}, active: "nexterm-server.service"})
	expect(t, s.read("var/lib/nexterm/data.db") == dbBeforeUpgrade, "database not restored")
	expect(t, s.read("var/lib/nexterm/data.db-wal") == walBeforeUpgrade, "WAL not restored")
	expect(t, !s.exists("var/lib/nexterm/data.db-shm"), "a sidecar absent from the snapshot must not survive the restore")
	expect(t, s.read("opt/nexterm/.rollback.failed/db/nexterm/data.db") == "migrated-by-v2\n", "the replaced database must be kept")
	expect(t, s.read("opt/nexterm/.rollback.failed/db/nexterm/data.db-shm") == "shm-by-v2\n", "the replaced sidecar must be kept")
	calls := s.systemctlCalls()
	expect(t, contains(calls, "stop nexterm-server.service") && contains(calls, "restart nexterm-server.service"), "calls: %v", calls)
}

func TestSystemdRollbackKeepDatabase(t *testing.T) {
	s, v2, _ := upgradeWithDatabase(t)
	s.write("var/lib/nexterm/data.db", "migrated-by-v2\n")
	got := s.mustInstall(v2, installOptions{args: []string{"--rollback", "--keep-database"}})
	expect(t, strings.Contains(got.stdout, "keeping the current database"), "stdout: %s", got.stdout)
	expect(t, s.read("var/lib/nexterm/data.db") == "migrated-by-v2\n", "--keep-database must not touch the database")
	expect(t, strings.Contains(s.read("opt/nexterm/nexterm-server"), "v1"), "program files must still roll back")
}
