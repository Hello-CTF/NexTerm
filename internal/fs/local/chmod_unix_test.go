//go:build unix

package local

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestChmodAndAtomicWritePreserveMode(t *testing.T) {
	filesystem := New()
	path := filepath.Join(t.TempDir(), "script.sh")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	mode := fs.ModeSetuid | fs.ModeSticky | 0o751
	if err := filesystem.Chmod(context.Background(), path, mode); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFile(context.Background(), path, []byte("new"), true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o751 || info.Mode()&fs.ModeSetuid == 0 || info.Mode()&fs.ModeSticky == 0 {
		t.Fatalf("mode = %o", info.Mode())
	}
	backup, err := os.Stat(path + BackupSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if backup.Mode().Perm() != 0o751 || backup.Mode()&fs.ModeSetuid == 0 || backup.Mode()&fs.ModeSticky == 0 {
		t.Fatalf("backup mode = %o", backup.Mode())
	}
}
