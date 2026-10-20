package production

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestProductionRedisOverConnectedSSHIntegration(t *testing.T) {
	if os.Getenv(dbTunnelIntegrationFlag) != "1" {
		t.Skipf("set %s=1 and NEXTERM_TEST_DB_BACKEND=redis to run the real Redis/SSH tunnel test", dbTunnelIntegrationFlag)
	}
	if backend := os.Getenv("NEXTERM_TEST_DB_BACKEND"); backend != "redis" {
		t.Skipf("NEXTERM_TEST_DB_BACKEND=%q does not select the Redis tunnel test", backend)
	}
	address := os.Getenv("NEXTERM_TEST_REDIS_ADDR")
	if address == "" {
		address = "127.0.0.1:6379"
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		t.Fatalf("NEXTERM_TEST_REDIS_ADDR: %v", err)
	}
	password := os.Getenv("NEXTERM_TEST_REDIS_PASSWORD")
	username := os.Getenv("NEXTERM_TEST_REDIS_USERNAME")
	const remoteHost = "redis.tunnel.invalid"
	remoteAddress := net.JoinHostPort(remoteHost, "6379")
	server := newDBTunnelIntegrationSSHServerFor(t, address, remoteAddress)
	manager := newDBTunnelIntegrationManager(t, server)
	database := newDBTunnelTestStore(t)
	owner := ipc.WithUserID(t.Context(), "owner-a")
	foreign := ipc.WithUserID(t.Context(), "owner-b")
	via := createDBTunnelIntegrationVia(t, database, owner)
	connectedSSH, err := manager.Connect(owner, session.Asset{ID: via.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatalf("connect SSH: %v", err)
	}
	service := newProductionDatabaseService(database, dbTunnelIntegrationPassword(password), manager)
	t.Cleanup(func() { _ = service.Close() })
	target := createDBTunnelIntegrationRedisAsset(t, database, owner, via.ID, remoteHost, username)

	directCtx, cancelDirect := context.WithTimeout(t.Context(), 3*time.Second)
	direct, directErr := (&net.Dialer{}).DialContext(directCtx, "tcp", remoteAddress)
	cancelDirect()
	if directErr == nil {
		_ = direct.Close()
		t.Fatal("database-only hostname was reachable without the SSH tunnel")
	}
	t.Logf("direct database-only hostname rejected as expected: %v", directErr)

	connectCtx, cancelConnect := context.WithCancel(owner)
	connected, err := service.Connect(connectCtx, db.ConnectArgs{AssetID: target.ID})
	cancelConnect()
	if err != nil {
		t.Fatalf("connect Redis through SSH: %v", err)
	}
	if output, err := service.RedisCommand(owner, connected.ConnID, []string{"PING"}); err != nil || output != "PONG" {
		t.Fatalf("Redis PING through SSH: output=%q err=%v", output, err)
	}
	const key = "nexterm:s2:ssh-tunnel"
	if output, err := service.RedisCommand(owner, connected.ConnID, []string{"SET", key, "through-ssh"}); err != nil || output != "OK" {
		t.Fatalf("Redis SET through SSH: output=%q err=%v", output, err)
	}
	view, err := service.RedisInspect(owner, connected.ConnID, key)
	if err != nil || view.KeyType != "string" || view.Value != "through-ssh" {
		t.Fatalf("Redis inspect through SSH: view=%+v err=%v", view, err)
	}
	if got := server.channelCount(1); got == 0 {
		t.Fatal("first SSH connection carried no direct-tcpip channels")
	}
	oldID, err := redisTunnelClientID(service, owner, connected.ConnID)
	if err != nil {
		t.Fatalf("Redis CLIENT ID: %v", err)
	}

	channels := server.channelCount(1)
	_, err = service.RedisCommand(foreign, connected.ConnID, []string{"PING"})
	requireDBTunnelCode(t, err, ipc.CodeForbidden)
	foreignTarget := createDBTunnelIntegrationRedisAsset(t, database, foreign, via.ID, remoteHost, username)
	_, err = service.Connect(foreign, db.ConnectArgs{AssetID: foreignTarget.ID})
	requireDBTunnelCode(t, err, ipc.CodeNotFound)
	if server.channelCount(1) != channels {
		t.Fatal("cross-owner rejection opened an SSH channel")
	}

	if err := manager.Reconnect(owner, connectedSSH.ID); err != nil {
		t.Fatalf("reconnect SSH: %v", err)
	}
	newID := oldID
	deadline := time.Now().Add(10 * time.Second)
	for {
		newID, err = redisTunnelClientID(service, owner, connected.ConnID)
		if err == nil && newID != oldID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("redial after SSH reconnect: oldID=%d newID=%d err=%v", oldID, newID, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := server.channelCount(2); got == 0 {
		t.Fatal("replacement SSH connection carried no Redis redial")
	}
	if got := server.channelCount(1); got != channels {
		t.Fatalf("stale SSH connection channels = %d, want %d", got, channels)
	}
	if output, err := service.RedisCommand(owner, connected.ConnID, []string{"DEL", key}); err != nil || output != "1" {
		t.Fatalf("Redis cleanup through SSH: output=%q err=%v", output, err)
	}
	t.Logf("SSH reconnect redialed Redis: client ID %d -> %d", oldID, newID)
}

func redisTunnelClientID(service *db.Service, ctx context.Context, connID string) (int64, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	output, err := service.RedisCommand(commandCtx, connID, []string{"CLIENT", "ID"})
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(output, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse Redis CLIENT ID %q: %w", output, err)
	}
	return id, nil
}

func createDBTunnelIntegrationRedisAsset(t *testing.T, database *store.Store, ctx context.Context, viaID, host, username string, tlsOptions ...*db.TLSOptions) store.AssetRow {
	t.Helper()
	credentialID, err := database.CredentialPut(ctx, store.CredentialInput{Name: "Redis", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}})
	if err != nil {
		t.Fatal(err)
	}
	options := map[string]any{"database": "0", "viaAssetId": viaID}
	if len(tlsOptions) > 0 && tlsOptions[0] != nil {
		options["tls"] = tlsOptions[0]
	}
	optionsJSON, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	port := int32(6379)
	asset, err := database.AssetCreate(ctx, store.AssetInput{
		Kind: "redis", Name: "Redis through SSH", Host: &host, Port: &port, Username: &username,
		CredID: &credentialID, OptionsJSON: string(optionsJSON),
	})
	if err != nil {
		t.Fatal(err)
	}
	return asset
}
