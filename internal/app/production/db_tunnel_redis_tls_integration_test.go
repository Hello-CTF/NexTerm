package production

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	redisDriver "github.com/redis/go-redis/v9"
)

func TestProductionRedisTLSOverConnectedSSHIntegration(t *testing.T) {
	if os.Getenv(dbTunnelIntegrationFlag) != "1" {
		t.Skipf("set %s=1 and NEXTERM_TEST_DB_BACKEND=redis to run the real Redis TLS/SSH tunnel test", dbTunnelIntegrationFlag)
	}
	if backend := os.Getenv("NEXTERM_TEST_DB_BACKEND"); backend != "redis" {
		t.Skipf("NEXTERM_TEST_DB_BACKEND=%q does not select the Redis TLS tunnel test", backend)
	}
	const remoteHost = "redis-tls.tunnel.invalid"
	remoteAddress := net.JoinHostPort(remoteHost, "6379")
	redisServer := startDBTunnelIntegrationTLSRedis(t, remoteHost)
	server := newDBTunnelIntegrationSSHServerFor(t, redisServer.address, remoteAddress)
	manager := newDBTunnelIntegrationManager(t, server)
	database := newDBTunnelTestStore(t)
	owner := ipc.WithUserID(t.Context(), "owner-a")
	via := createDBTunnelIntegrationVia(t, database, owner)
	connectedSSH, err := manager.Connect(owner, session.Asset{ID: via.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatalf("connect SSH: %v", err)
	}
	service := newProductionDatabaseService(database, dbTunnelIntegrationPassword(redisServer.password), manager)
	t.Cleanup(func() { _ = service.Close() })
	target := createDBTunnelIntegrationRedisAsset(t, database, owner, via.ID, remoteHost, "", &db.TLSOptions{
		Mode: "required", CAFile: redisServer.caFile,
	})

	connectCtx, cancelConnect := context.WithCancel(owner)
	connected, err := service.Connect(connectCtx, db.ConnectArgs{AssetID: target.ID})
	cancelConnect()
	if err != nil {
		t.Fatalf("connect TLS-only Redis through SSH: %v", err)
	}
	if output, err := service.RedisCommand(owner, connected.ConnID, []string{"PING"}); err != nil || output != "PONG" {
		t.Fatalf("Redis TLS PING through SSH: output=%q err=%v", output, err)
	}
	oldID, err := redisTunnelClientID(service, owner, connected.ConnID)
	if err != nil {
		t.Fatalf("Redis TLS CLIENT ID: %v", err)
	}
	t.Logf("TLS-only Redis accepted the verified SSH tunnel connection")

	expectCertificateReject := func(name string, tlsOptions *db.TLSOptions) {
		t.Run(name, func(t *testing.T) {
			channels := server.channelCount(1)
			rejected := createDBTunnelIntegrationRedisAsset(t, database, owner, via.ID, remoteHost, "", tlsOptions)
			result, err := service.Connect(owner, db.ConnectArgs{AssetID: rejected.ID})
			if err == nil {
				_ = service.Disconnect(owner, result.ConnID)
				t.Fatal("invalid Redis TLS certificate configuration was accepted")
			}
			if server.channelCount(1) == channels {
				t.Fatal("certificate rejection did not reach the SSH/TLS dial")
			}
			t.Logf("rejected as expected: %v", err)
		})
	}
	expectCertificateReject("wrong hostname", &db.TLSOptions{
		Mode: "required", ServerName: "wrong.tunnel.invalid", CAFile: redisServer.caFile,
	})
	_, untrustedCA := writeDBTunnelIntegrationTLSFilesForHost(t, remoteHost)
	expectCertificateReject("untrusted CA", &db.TLSOptions{Mode: "required", CAFile: untrustedCA})

	channels := server.channelCount(1)
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
			t.Fatalf("TLS redial after SSH reconnect: oldID=%d newID=%d err=%v", oldID, newID, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := server.channelCount(2); got == 0 {
		t.Fatal("replacement SSH connection carried no Redis TLS redial")
	}
	if got := server.channelCount(1); got != channels {
		t.Fatalf("stale SSH connection channels = %d, want %d", got, channels)
	}
	t.Logf("SSH reconnect repeated the TLS handshake: client ID %d -> %d", oldID, newID)
}

type dbTunnelIntegrationRedisTLS struct {
	address  string
	caFile   string
	password string
}

func startDBTunnelIntegrationTLSRedis(t *testing.T, serverName string) dbTunnelIntegrationRedisTLS {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker is required for the real TLS Redis test: %v", err)
	}
	dir, caFile := writeDBTunnelIntegrationTLSFilesForHost(t, serverName)
	const password = "db-tunnel-redis-tls-password"
	name := fmt.Sprintf("nexterm-redis-tls-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	output, err := exec.CommandContext(ctx, "docker", "run", "--detach", "--rm", "--name", name,
		"--publish", "127.0.0.1::6380",
		"--volume", dir+":/tls:ro",
		"redis:7.4",
		"redis-server",
		"--port", "0",
		"--tls-port", "6380",
		"--tls-cert-file", "/tls/server.crt",
		"--tls-key-file", "/tls/server.key",
		"--tls-ca-cert-file", "/tls/ca.crt",
		"--tls-auth-clients", "no",
		"--requirepass", password,
		"--appendonly", "no",
	).CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("start TLS Redis: %v: %s", err, output)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "docker", "rm", "--force", name).Run()
	})
	ctx, cancel = context.WithTimeout(t.Context(), 15*time.Second)
	output, err = exec.CommandContext(ctx, "docker", "port", name, "6380/tcp").CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("inspect TLS Redis address: %v: %s", err, output)
	}
	address := strings.TrimSpace(strings.Split(string(output), "\n")[0])
	if _, _, err := net.SplitHostPort(address); err != nil {
		t.Fatalf("parse TLS Redis address %q: %v", address, err)
	}
	client := redisDriver.NewClient(&redisDriver.Options{
		Addr: address, Password: password, TLSConfig: dbTunnelIntegrationTLSConfig(t, caFile, serverName),
		DialTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, MaxRetries: -1, ContextTimeoutEnabled: true,
	})
	defer client.Close()
	deadline := time.Now().Add(60 * time.Second)
	for {
		pingCtx, cancelPing := context.WithTimeout(t.Context(), 3*time.Second)
		err = client.Ping(pingCtx).Err()
		cancelPing()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			logs, _ := exec.CommandContext(context.Background(), "docker", "logs", "--tail", "40", name).CombinedOutput()
			t.Fatalf("TLS Redis did not become ready: %v\n%s", err, logs)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return dbTunnelIntegrationRedisTLS{address: address, caFile: caFile, password: password}
}
