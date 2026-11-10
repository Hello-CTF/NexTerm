//go:build !windows

package installer

import (
	"strings"
	"testing"
)

const pathsDropin = "etc/systemd/system/nexterm-onlyserver.service.d/50-nexterm-paths.conf"

func TestSystemdDefaultPathsNeedNoDropin(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.seedFirstInstall()
	expect(t, !s.exists(pathsDropin), "default paths must not get a drop-in")
}

func TestSystemdCustomDataDirGetsWritablePathsDropin(t *testing.T) {
	s := newSandbox(t, stubOptions{})
	s.seedFirstInstall()
	s.write("etc/nexterm/onlyserver.env", "NEXTERM_AUTH=on\nNEXTERM_DATA_DIR=\"/srv/nexterm data/100%\"\nNEXTERM_MASTER_KEY_FILE=/home/ops/nexterm.key\nNEXTERM_DB=sqlite\n")
	s.write("home/ops/nexterm.key", fakeKeyHex+"\n")
	v2 := s.payload("v2")
	s.mustInstall(v2, installOptions{})
	dropin := s.read(pathsDropin)
	lines := strings.Split(dropin, "\n")
	expect(t, contains(lines, "[Service]"), "drop-in: %s", dropin)
	expect(t, contains(lines, `ReadWritePaths="-/srv/nexterm data/100%%"`), "data dir must be writable with escaped specifiers: %s", dropin)
	expect(t, contains(lines, "ProtectHome=read-only"), "a key file under /home must stay readable: %s", dropin)

	// Rolling back to a payload installed without the drop-in removes it again.
	s.mustInstall(v2, installOptions{args: []string{"--rollback"}})
	expect(t, !s.exists(pathsDropin), "rollback must remove a drop-in the previous payload did not have")

	// Back on the default paths, a later install drops the stale drop-in.
	s.mustInstall(v2, installOptions{})
	expect(t, s.exists(pathsDropin), "reinstall must recreate the drop-in")
	s.write("etc/nexterm/onlyserver.env", "NEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_MASTER_KEY_FILE=/etc/nexterm/master.key\nNEXTERM_DB=sqlite\n")
	s.mustInstall(v2, installOptions{})
	expect(t, !s.exists(pathsDropin), "drop-in must be removed once the defaults cover the paths")
}
