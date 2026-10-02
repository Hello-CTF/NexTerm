package mount

import "testing"

func TestParseWindowsNetUse(t *testing.T) {
	output := "New connections will be remembered.\r\n\r\n" +
		"Status       Local     Remote                    Network\r\n" +
		"-------------------------------------------------------------------------------\r\n" +
		"OK           Z:        \\\\server\\share           Microsoft Windows Network\r\n" +
		"Disconnected Y:        \\\\other\\data            Microsoft Windows Network\r\n" +
		"命令成功完成。\r\n"
	entries := ParseTable("windows", output)
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].LocalPoint != "Y:" || entries[0].Remote != `\\other\data` || entries[1].LocalPoint != "Z:" || entries[1].Remote != `\\server\share` {
		t.Fatalf("entries = %+v", entries)
	}
	for _, entry := range entries {
		if entry.ID != "" || entry.SessionID != nil || entry.CreatedAt != nil {
			t.Fatalf("scanned metadata = %+v", entry)
		}
	}
}

func TestParseLinuxMountAndOctalEscapes(t *testing.T) {
	output := "tmpfs on /tmp type tmpfs (rw)\n" +
		`alice@example:/srv/remote\040dir on /mnt/remote\040dir type fuse.sshfs (rw,nosuid,nodev)` + "\n" +
		"encfs on /mnt/private type fuse.encfs (rw)\n"
	entries := ParseTable("linux", output)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Remote != "alice@example:/srv/remote dir" || entries[0].LocalPoint != "/mnt/remote dir" {
		t.Fatalf("entry = %+v", entries[0])
	}
}

func TestParseMacOSMountFamilies(t *testing.T) {
	output := `/dev/disk3s1 on / (apfs, local, journaled)` + "\n" +
		`other@example:/other on /Volumes/Other (notmacfuse, nodev, nosuid)` + "\n" +
		`alice@example:/one on /Volumes/One (osxfuse, nodev, nosuid)` + "\n" +
		`bob@example:/two on /Volumes/Two\040Disk (macfuse, nodev, nosuid)` + "\n"
	entries := ParseTable("darwin", output)
	if len(entries) != 2 || entries[0].LocalPoint != "/Volumes/One" || entries[1].LocalPoint != "/Volumes/Two Disk" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestParseRejectsMalformedAndUnrelatedRows(t *testing.T) {
	for _, platform := range []string{"windows", "linux", "darwin"} {
		if entries := ParseTable(platform, "\nnot a mount\n"); len(entries) != 0 {
			t.Fatalf("%s entries = %+v", platform, entries)
		}
	}
}
