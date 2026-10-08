package production

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/ssh"
)

func TestProductionHostKeyRemoveKeepsIndentedFileFormat(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database, err := openProductionStore(ctx, ProductionConfig{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	path := filepath.Join(dir, "hostkeys.json")
	keys, err := newProductionHostKeyStore(path, database)
	if err != nil {
		t.Fatal(err)
	}
	first := ssh.HostKey{Host: "a.test", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "SHA256:first", Key: "a2V5LW9uZQ=="}
	second := ssh.HostKey{Host: "b.test", Port: 2222, KeyType: "ssh-ed25519", Fingerprint: "SHA256:second", Key: "a2V5LXR3bw=="}
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
