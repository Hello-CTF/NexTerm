package production

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
)

func TestProductionHostKeyConnectorStoreBackedOwner(t *testing.T) {
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "owner-a")
	ownerB := ipc.WithUserID(ctx, "owner-b")
	database := newKnownHostOwnerTestDB(t)
	hostKeys, err := newProductionHostKeyStore(ctx, filepath.Join(t.TempDir(), "known_hosts.json"), database, false)
	if err != nil {
		t.Fatal(err)
	}
	connector := &productionConnector{database: database, hostKeys: hostKeys}
	server := newHostKeyTestServer(t)
	host := "127.0.0.1"
	port := int32(server.port())
	username := "test"
	authKind := "password"
	row, err := database.AssetCreate(ownerA, store.AssetInput{
		Kind: "ssh", Name: "connector-owner", Host: &host, Port: &port, Username: &username, AuthKind: &authKind,
		OptionsJSON: `{"password":"host-key-test","userId":"owner-b"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	asset, err := productionSessionAsset(row, false, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	transport, err := connector.Connect(context.Background(), asset, 1)
	if transport != nil {
		_ = transport.Close()
	}
	if !errors.Is(err, ssh.ErrHostKeyPending) {
		t.Fatalf("store-backed connector did not use the database owner for host key verification: %v", err)
	}
	publicKey := (*server.signer.Load()).PublicKey()
	if err := database.KnownHostAccept(ownerA, host, port, publicKey.Type(), gossh.FingerprintSHA256(publicKey)); err != nil {
		t.Fatal(err)
	}
	transport, err = connector.Connect(context.Background(), asset, 1)
	if err != nil {
		t.Fatalf("database owner approval was not applied: %v", err)
	}
	_ = transport.Close()
	if _, found, err := database.KnownHostGet(ownerA, host, port, publicKey.Type()); err != nil || !found {
		t.Fatalf("owner A approval missing: found=%v err=%v", found, err)
	}
	if _, found, err := database.KnownHostGet(ownerB, host, port, publicKey.Type()); err != nil || found {
		t.Fatalf("client-supplied userId changed trust ownership: found=%v err=%v", found, err)
	}
	anonymousAsset, err := productionSessionAsset(row, false)
	if err != nil {
		t.Fatal(err)
	}
	anonymousOptions := anonymousAsset.Options.(map[string]any)
	if _, exists := anonymousOptions["userId"]; exists {
		t.Fatal("untrusted asset userId survived conversion")
	}
	transport, err = connector.Connect(context.Background(), anonymousAsset, 1)
	if transport != nil {
		_ = transport.Close()
	}
	if err == nil || errors.Is(err, ssh.ErrHostKeyPending) {
		t.Fatalf("anonymous connector used the asset owner trust: %v", err)
	}
}
