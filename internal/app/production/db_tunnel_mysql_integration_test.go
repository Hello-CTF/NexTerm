package production

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	mysqlDriver "github.com/go-sql-driver/mysql"
)

func TestProductionMySQLOverConnectedSSHIntegration(t *testing.T) {
	if os.Getenv(dbTunnelIntegrationFlag) != "1" {
		t.Skipf("set %s=1, NEXTERM_TEST_DB_BACKEND=mysql|mariadb and NEXTERM_TEST_DB_DSN to run real MySQL/SSH tunnel tests", dbTunnelIntegrationFlag)
	}
	backend := os.Getenv("NEXTERM_TEST_DB_BACKEND")
	if backend != "mysql" && backend != "mariadb" {
		t.Skipf("NEXTERM_TEST_DB_BACKEND=%q does not select the MySQL/MariaDB tunnel test", backend)
	}
	dsn := os.Getenv("NEXTERM_TEST_DB_DSN")
	if dsn == "" {
		t.Fatal("NEXTERM_TEST_DB_DSN is required for the real MySQL/MariaDB SSH tunnel test")
	}
	config, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse NEXTERM_TEST_DB_DSN: %v", err)
	}
	if config.Net != "tcp" {
		t.Fatalf("test DSN network = %q, want tcp", config.Net)
	}

	remoteHost := backend + ".tunnel.invalid"
	remoteAddress := net.JoinHostPort(remoteHost, "3306")
	server := newDBTunnelIntegrationSSHServerFor(t, config.Addr, remoteAddress)
	manager := newDBTunnelIntegrationManager(t, server)
	database := newDBTunnelTestStore(t)
	owner := ipc.WithUserID(t.Context(), "owner-a")
	foreign := ipc.WithUserID(t.Context(), "owner-b")
	via := createDBTunnelIntegrationVia(t, database, owner)
	connectedSSH, err := manager.Connect(owner, session.Asset{ID: via.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatalf("connect SSH: %v", err)
	}
	service := newProductionDatabaseService(database, dbTunnelIntegrationPassword(config.Passwd), manager)
	t.Cleanup(func() { _ = service.Close() })
	target := createDBTunnelIntegrationMySQLAsset(t, database, owner, via.ID, remoteHost, config)

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
		t.Fatalf("connect %s through SSH: %v", backend, err)
	}
	query := func(statement string) db.QueryResult {
		t.Helper()
		result, err := service.Query(owner, connected.ConnID, statement, 0, 5*time.Second)
		if err != nil || result.Error != nil || len(result.Rows) == 0 {
			t.Fatalf("query %q through SSH: result=%+v err=%v", statement, result, err)
		}
		return result
	}
	result := query("SELECT VERSION()")
	version, ok := result.Rows[0][0].(string)
	if !ok {
		t.Fatalf("VERSION() type = %T", result.Rows[0][0])
	}
	if backend == "mysql" && !strings.HasPrefix(version, "8.4.") {
		t.Fatalf("MySQL version = %q, want 8.4.x", version)
	}
	if backend == "mariadb" && (!strings.HasPrefix(version, "11.4.") || !strings.Contains(version, "MariaDB")) {
		t.Fatalf("MariaDB version = %q, want 11.4.x MariaDB", version)
	}
	t.Logf("verified %s through SSH: %s", backend, version)
	if result = query("SELECT 1"); fmt.Sprint(result.Rows[0][0]) != "1" {
		t.Fatalf("SELECT 1 result = %+v", result)
	}
	if got := server.channelCount(1); got == 0 {
		t.Fatal("first SSH connection carried no direct-tcpip channels")
	}
	oldID := fmt.Sprint(query("SELECT CONNECTION_ID()").Rows[0][0])

	channels := server.channelCount(1)
	_, err = service.Query(foreign, connected.ConnID, "SELECT 1", 0, time.Second)
	requireDBTunnelCode(t, err, ipc.CodeForbidden)
	foreignTarget := createDBTunnelIntegrationMySQLAsset(t, database, foreign, via.ID, remoteHost, config)
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
	for newID == oldID {
		result, err := service.Query(owner, connected.ConnID, "SELECT CONNECTION_ID()", 0, 2*time.Second)
		if err == nil && result.Error == nil && len(result.Rows) == 1 {
			newID = fmt.Sprint(result.Rows[0][0])
		}
		if newID != oldID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("redial after SSH reconnect: oldID=%s result=%+v err=%v", oldID, result, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := server.channelCount(2); got == 0 {
		t.Fatal("replacement SSH connection carried no MySQL redial")
	}
	if got := server.channelCount(1); got != channels {
		t.Fatalf("stale SSH connection channels = %d, want %d", got, channels)
	}
	t.Logf("SSH reconnect redialed %s: connection ID %s -> %s", backend, oldID, newID)
}

func createDBTunnelIntegrationMySQLAsset(t *testing.T, database *store.Store, ctx context.Context, viaID, host string, config *mysqlDriver.Config) store.AssetRow {
	t.Helper()
	credentialID, err := database.CredentialPut(ctx, store.CredentialInput{Name: "MySQL/MariaDB", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}})
	if err != nil {
		t.Fatal(err)
	}
	optionsJSON, err := json.Marshal(map[string]any{"database": config.DBName, "viaAssetId": viaID})
	if err != nil {
		t.Fatal(err)
	}
	username, port := config.User, int32(3306)
	asset, err := database.AssetCreate(ctx, store.AssetInput{
		Kind: "mysql", Name: "MySQL/MariaDB through SSH", Host: &host, Port: &port, Username: &username,
		CredID: &credentialID, OptionsJSON: string(optionsJSON),
	})
	if err != nil {
		t.Fatal(err)
	}
	return asset
}
