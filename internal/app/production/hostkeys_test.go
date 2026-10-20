package production

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

func TestProductionHostKeyRemoveKeepsIndentedFileFormat(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(dir, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	path := filepath.Join(dir, "hostkeys.json")
	keys, err := newProductionHostKeyStore(ctx, path, database, true)
	if err != nil {
		t.Fatal(err)
	}
	first := knownHostOwnerTestKey("a.test", "SHA256:first", "first-key")
	second := knownHostOwnerTestKey("b.test", "SHA256:second", "second-key")
	second.Port = 2222
	for _, key := range []ssh.HostKey{first, second} {
		if err := keys.PutHostKey(ctx, key, false); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := database.KnownHostList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var removedID string
	for _, row := range rows {
		if row.Fingerprint == first.Fingerprint {
			removedID = row.ID
		}
	}
	if removedID == "" {
		t.Fatal("accepted host key row not found")
	}
	if err := keys.Remove(ctx, removedID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("\n  \"version\": 1")) {
		t.Fatalf("rewritten host key store is not indented JSON:\n%s", data)
	}
	var file productionHostKeyFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Keys) != 1 || file.Keys[0].Fingerprint != second.Fingerprint {
		t.Fatalf("rewritten keys = %+v", file.Keys)
	}
	remaining, err := keys.HostKeys(ctx, second.Host, second.Port)
	if err != nil || len(remaining) != 1 {
		t.Fatalf("remaining keys = %+v, %v", remaining, err)
	}
	rows, err = database.KnownHostList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Fingerprint != second.Fingerprint {
		t.Fatalf("remaining ledger rows = %+v", rows)
	}
}

func TestProductionHostKeyAcceptFailureRestoresFile(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "replacement"}[replace], func(t *testing.T) {
			ctx := t.Context()
			dir := t.TempDir()
			databasePath := filepath.Join(dir, "data.db")
			database, err := store.Open(ctx, databasePath)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "hostkeys.json")
			keys, err := newProductionHostKeyStore(ctx, path, database, true)
			if err != nil {
				t.Fatal(err)
			}
			original := ssh.HostKey{Host: "a.test", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "SHA256:first", Key: "a2V5LW9uZQ=="}
			if replace {
				if err := keys.PutHostKey(ctx, original, false); err != nil {
					t.Fatal(err)
				}
			}
			before, readErr := os.ReadFile(path)
			if replace && readErr != nil || !replace && !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("read original file: %v", readErr)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			changed := original
			changed.Fingerprint = "SHA256:changed"
			changed.Key = "a2V5LW5ldw=="
			if err := keys.PutHostKey(ctx, changed, replace); err == nil {
				t.Fatal("closed database unexpectedly accepted host key")
			}
			after, readErr := os.ReadFile(path)
			if replace {
				if readErr != nil || !bytes.Equal(after, before) {
					t.Fatalf("file changed after failed replacement: %v, before=%q, after=%q", readErr, before, after)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("new trust file remains after database failure: %v, %q", readErr, after)
			}
			persisted, err := store.Open(ctx, databasePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = persisted.Close() })
			rows, err := persisted.KnownHostList(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if replace && (len(rows) != 1 || rows[0].Fingerprint != original.Fingerprint) || !replace && len(rows) != 0 {
				t.Fatalf("database changed after failed acceptance: %+v", rows)
			}
		})
	}
}

func TestProductionHostKeyRemoveFailureRestoresFile(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(dir, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	path := filepath.Join(dir, "hostkeys.json")
	keys, err := newProductionHostKeyStore(ctx, path, database, true)
	if err != nil {
		t.Fatal(err)
	}
	key := ssh.HostKey{Host: "a.test", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "SHA256:first", Key: "a2V5LW9uZQ=="}
	if err := keys.PutHostKey(ctx, key, false); err != nil {
		t.Fatal(err)
	}
	rows, err := database.KnownHostList(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("host keys = %+v, %v", rows, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().ExecContext(ctx, `CREATE TRIGGER fail_known_host_remove BEFORE DELETE ON known_host BEGIN SELECT RAISE(ABORT, 'remove failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := keys.Remove(ctx, rows[0].ID); err == nil {
		t.Fatal("database failure unexpectedly removed the host key")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("host key file changed after failed database removal: %v", err)
	}
	remaining, err := database.KnownHostList(ctx)
	if err != nil || len(remaining) != 1 || remaining[0].ID != rows[0].ID {
		t.Fatalf("host key approval changed after failed removal: %+v, %v", remaining, err)
	}
}
