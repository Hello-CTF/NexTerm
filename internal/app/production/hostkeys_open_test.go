package production

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
)

func TestProductionHostKeyOpenSSHUsesLegacyFile(t *testing.T) {
	server := newHostKeyTestServer(t)
	signer := *server.signer.Load()
	publicKey := signer.PublicKey()
	legacy := ssh.HostKey{
		Host: "127.0.0.1", Port: server.port(), KeyType: publicKey.Type(),
		Fingerprint: gossh.FingerprintSHA256(publicKey), Key: base64.StdEncoding.EncodeToString(publicKey.Marshal()),
	}
	dataDir := t.TempDir()
	legacyPath := filepath.Join(dataDir, "known_hosts.json")
	if err := writeProductionHostKeyFile(legacyPath, productionHostKeyFile{Version: 1, Keys: []ssh.HostKey{legacy}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewProduction(owner.WithOpenAccess(context.Background()), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{},
		},
		DataDir: dataDir,
		Desktop: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })
	assetID := createHostKeyTestAsset(t, application, server.port())
	connected := connectHostKeyTestAsset(t, application, assetID)
	if connected.ID == "" {
		t.Fatal("explicit Open did not trust the legacy host key during SSH")
	}
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, application, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
	after, err := os.ReadFile(legacyPath)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("explicit Open rewrote the legacy trust file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "known_hosts")); !os.IsNotExist(err) {
		t.Fatalf("explicit Open created an account namespace: %v", err)
	}
	if _, err := os.Stat(legacyPath + ".pre-owner-migration.bak"); !os.IsNotExist(err) {
		t.Fatalf("explicit Open ran account migration: %v", err)
	}
}
