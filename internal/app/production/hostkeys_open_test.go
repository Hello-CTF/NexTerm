package production

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// dispatchHostKeyOpenTest mirrors how the application marks an admitted open request — the owner-open
// deployment mark plus the session-open grant — so the session manager's open boundary admits the
// identity-less dispatch exactly as it does for a real desktop/open deployment, while every other
// request shape stays fail-closed.
func dispatchHostKeyOpenTest(t *testing.T, application *Production, command, args string) ipc.Response {
	t.Helper()
	ctx := owner.WithOpenAccess(session.WithOpenAccess(t.Context()))
	return application.Dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args), ClientID: "client-a"}, application.Environment("client-a"))
}

func createHostKeyOpenTestAsset(t *testing.T, application *Production, port int) string {
	t.Helper()
	response := dispatchHostKeyOpenTest(t, application, "asset_create", `{"args":{"kind":"ssh","name":"host-key-test","host":"127.0.0.1","port":`+strconv.Itoa(port)+`,"username":"test","authKind":"password","options":{"password":"host-key-test-secret"}}}`)
	var created assetDTO
	requireStoreTestResponse(t, response, &created)
	return created.ID
}

func connectHostKeyOpenTestAsset(t *testing.T, application *Production, assetID string) sessionInfoDTO {
	t.Helper()
	response := dispatchHostKeyOpenTest(t, application, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	var connected sessionInfoDTO
	requireStoreTestResponse(t, response, &connected)
	return connected
}

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
	assetID := createHostKeyOpenTestAsset(t, application, server.port())
	connected := connectHostKeyOpenTestAsset(t, application, assetID)
	if connected.ID == "" {
		t.Fatal("explicit Open did not trust the legacy host key during SSH")
	}
	requireProductionNullHostKey(t, dispatchHostKeyOpenTest(t, application, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
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
