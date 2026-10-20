package production

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	transportssh "github.com/Hello-CTF/NexTerm/internal/transport/ssh"
	"github.com/jackc/pgx/v5"
	gossh "golang.org/x/crypto/ssh"
)

const (
	dbTunnelIntegrationFlag = "NEXTERM_DB_TUNNEL_INTEGRATION"
	dbTunnelIntegrationHost = "postgres.tunnel.invalid"
)

type dbTunnelIntegrationPassword string

func (p dbTunnelIntegrationPassword) DecryptCredentialString(context.Context, store.CredentialRow) (string, error) {
	return string(p), nil
}

func TestProductionPostgresOverConnectedSSHIntegration(t *testing.T) {
	if os.Getenv(dbTunnelIntegrationFlag) != "1" {
		t.Skipf("set %s=1 and NEXTERM_TEST_DB_DSN to run real PostgreSQL/SSH tunnel tests", dbTunnelIntegrationFlag)
	}
	if backend := os.Getenv("NEXTERM_TEST_DB_BACKEND"); backend != "" && backend != "postgres" {
		t.Skipf("NEXTERM_TEST_DB_BACKEND=%s selects another database tunnel test", backend)
	}
	dsn := os.Getenv("NEXTERM_TEST_DB_DSN")
	if dsn == "" {
		t.Fatal("NEXTERM_TEST_DB_DSN is required for the real PostgreSQL/SSH tunnel test")
	}
	existing, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse NEXTERM_TEST_DB_DSN: %v", err)
	}

	t.Run("ping query reconnect and owner", func(t *testing.T) {
		server := newDBTunnelIntegrationSSHServer(t, net.JoinHostPort(existing.Host, strconv.Itoa(int(existing.Port))))
		manager := newDBTunnelIntegrationManager(t, server)
		database := newDBTunnelTestStore(t)
		owner := ipc.WithUserID(t.Context(), "owner-a")
		foreign := ipc.WithUserID(t.Context(), "owner-b")
		via := createDBTunnelIntegrationVia(t, database, owner)
		connectedSSH, err := manager.Connect(owner, session.Asset{ID: via.ID, Kind: session.KindSSH})
		if err != nil {
			t.Fatalf("connect SSH: %v", err)
		}
		service := newProductionDatabaseService(database, dbTunnelIntegrationPassword(existing.Password), manager)
		t.Cleanup(func() { _ = service.Close() })
		target := createDBTunnelIntegrationAsset(t, database, owner, via.ID, existing, nil)

		directConfig := existing.Copy()
		directConfig.Host = dbTunnelIntegrationHost
		directConfig.Port = 5432
		directConfig.Fallbacks = nil
		directConfig.ConnectTimeout = 2 * time.Second
		directCtx, cancelDirect := context.WithTimeout(t.Context(), 3*time.Second)
		direct, directErr := pgx.ConnectConfig(directCtx, directConfig)
		cancelDirect()
		if directErr == nil {
			_ = direct.Close(t.Context())
			t.Fatal("database-only hostname was reachable without the SSH tunnel")
		}
		t.Logf("direct database-only hostname rejected as expected: %v", directErr)

		connectCtx, cancelConnect := context.WithCancel(owner)
		connected, err := service.Connect(connectCtx, db.ConnectArgs{AssetID: target.ID})
		cancelConnect()
		if err != nil {
			t.Fatalf("connect through SSH: %v", err)
		}
		result, err := service.Query(owner, connected.ConnID, "SELECT 1", 0, 5*time.Second)
		if err != nil || result.Error != nil || len(result.Rows) != 1 || result.Rows[0][0] != int64(1) {
			t.Fatalf("query through SSH: result=%+v err=%v", result, err)
		}
		if got := server.channelCount(1); got == 0 {
			t.Fatal("first SSH connection carried no direct-tcpip channels")
		}

		result, err = service.Query(owner, connected.ConnID, "SELECT pg_backend_pid()", 0, 5*time.Second)
		if err != nil || result.Error != nil || len(result.Rows) != 1 {
			t.Fatalf("backend PID: result=%+v err=%v", result, err)
		}
		oldPID, ok := result.Rows[0][0].(int64)
		if !ok {
			t.Fatalf("backend PID type = %T", result.Rows[0][0])
		}
		adminCtx, cancelAdmin := context.WithTimeout(t.Context(), 10*time.Second)
		admin, err := pgx.ConnectConfig(adminCtx, existing.Copy())
		if err != nil {
			cancelAdmin()
			t.Fatalf("direct test-admin connection: %v", err)
		}
		var terminated bool
		err = admin.QueryRow(adminCtx, "SELECT pg_terminate_backend($1)", oldPID).Scan(&terminated)
		closeErr := admin.Close(adminCtx)
		cancelAdmin()
		if err != nil || !terminated || closeErr != nil {
			t.Fatalf("terminate old backend: terminated=%v err=%v close=%v", err, terminated, closeErr)
		}
		oldChannels := server.channelCount(1)
		if err := manager.Reconnect(owner, connectedSSH.ID); err != nil {
			t.Fatalf("reconnect SSH: %v", err)
		}

		var newPID int64
		deadline := time.Now().Add(10 * time.Second)
		for {
			result, err = service.Query(owner, connected.ConnID, "SELECT pg_backend_pid()", 0, 2*time.Second)
			if err == nil && result.Error == nil && len(result.Rows) == 1 {
				newPID, ok = result.Rows[0][0].(int64)
				if ok {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("redial after SSH reconnect: result=%+v err=%v", result, err)
			}
			time.Sleep(50 * time.Millisecond)
		}
		if newPID == oldPID {
			t.Fatalf("backend PID remained %d; no physical redial occurred", oldPID)
		}
		if got := server.channelCount(2); got == 0 {
			t.Fatal("replacement SSH connection carried no PostgreSQL redial")
		}
		if got := server.channelCount(1); got != oldChannels {
			t.Fatalf("stale SSH connection channels = %d, want %d", got, oldChannels)
		}

		foreignTarget := createDBTunnelIntegrationAsset(t, database, foreign, via.ID, existing, nil)
		firstChannels, secondChannels := server.channelCount(1), server.channelCount(2)
		_, err = service.Connect(foreign, db.ConnectArgs{AssetID: foreignTarget.ID})
		requireDBTunnelCode(t, err, ipc.CodeNotFound)
		if server.channelCount(1) != firstChannels || server.channelCount(2) != secondChannels {
			t.Fatal("cross-owner rejection opened an SSH channel")
		}
	})

	t.Run("TLS uses original database hostname", func(t *testing.T) {
		postgres := startDBTunnelIntegrationTLSPostgres(t)
		server := newDBTunnelIntegrationSSHServer(t, net.JoinHostPort(postgres.config.Host, strconv.Itoa(int(postgres.config.Port))))
		manager := newDBTunnelIntegrationManager(t, server)
		database := newDBTunnelTestStore(t)
		owner := ipc.WithUserID(t.Context(), "owner-tls")
		via := createDBTunnelIntegrationVia(t, database, owner)
		if _, err := manager.Connect(owner, session.Asset{ID: via.ID, Kind: session.KindSSH}); err != nil {
			t.Fatalf("connect SSH: %v", err)
		}
		service := newProductionDatabaseService(database, dbTunnelIntegrationPassword(postgres.config.Password), manager)
		t.Cleanup(func() { _ = service.Close() })
		target := createDBTunnelIntegrationAsset(t, database, owner, via.ID, postgres.config, &db.TLSOptions{
			Mode: "required", CAFile: postgres.caFile,
		})
		connected, err := service.Connect(owner, db.ConnectArgs{AssetID: target.ID})
		if err != nil {
			t.Fatalf("verified TLS through SSH: %v", err)
		}
		result, err := service.Query(owner, connected.ConnID, "SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()", 0, 5*time.Second)
		if err != nil || result.Error != nil || len(result.Rows) != 1 || result.Rows[0][0] != true {
			t.Fatalf("TLS session query: result=%+v err=%v", result, err)
		}

		wrongName := createDBTunnelIntegrationAsset(t, database, owner, via.ID, postgres.config, &db.TLSOptions{
			Mode: "required", ServerName: "wrong.tunnel.invalid", CAFile: postgres.caFile,
		})
		if _, err := service.Connect(owner, db.ConnectArgs{AssetID: wrongName.ID}); err == nil {
			t.Fatal("TLS certificate with the wrong database hostname was accepted")
		}
	})
}

func createDBTunnelIntegrationVia(t *testing.T, database *store.Store, ctx context.Context) store.AssetRow {
	t.Helper()
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: session.KindSSH, Name: "SSH tunnel", OptionsJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	return asset
}

func createDBTunnelIntegrationAsset(t *testing.T, database *store.Store, ctx context.Context, viaID string, config *pgx.ConnConfig, tlsOptions *db.TLSOptions) store.AssetRow {
	t.Helper()
	credentialID, err := database.CredentialPut(ctx, store.CredentialInput{Name: "PostgreSQL", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}})
	if err != nil {
		t.Fatal(err)
	}
	options := map[string]any{"database": config.Database, "viaAssetId": viaID}
	if tlsOptions != nil {
		options["tls"] = tlsOptions
	}
	optionsJSON, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	host, username, port := dbTunnelIntegrationHost, config.User, int32(5432)
	asset, err := database.AssetCreate(ctx, store.AssetInput{
		Kind: "postgres", Name: "PostgreSQL through SSH", Host: &host, Port: &port, Username: &username,
		CredID: &credentialID, OptionsJSON: string(optionsJSON),
	})
	if err != nil {
		t.Fatal(err)
	}
	return asset
}

type dbTunnelIntegrationSSHServer struct {
	listener      net.Listener
	config        *gossh.ServerConfig
	target        string
	remoteAddress string
	nextID        atomic.Uint64
	mu            sync.Mutex
	conns         map[uint64]*gossh.ServerConn
	channels      map[uint64]int
}

func newDBTunnelIntegrationSSHServer(t *testing.T, target string) *dbTunnelIntegrationSSHServer {
	t.Helper()
	return newDBTunnelIntegrationSSHServerFor(t, target, net.JoinHostPort(dbTunnelIntegrationHost, "5432"))
}

func newDBTunnelIntegrationSSHServerFor(t *testing.T, target, remoteAddress string) *dbTunnelIntegrationSSHServer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &gossh.ServerConfig{PasswordCallback: func(metadata gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
		if metadata.User() == "test" && string(password) == "secret" {
			return nil, nil
		}
		return nil, errors.New("invalid SSH test credentials")
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dbTunnelIntegrationSSHServer{
		listener: listener, config: config, target: target, remoteAddress: remoteAddress,
		conns: make(map[uint64]*gossh.ServerConn), channels: make(map[uint64]int),
	}
	go server.accept()
	t.Cleanup(func() {
		_ = listener.Close()
		server.mu.Lock()
		defer server.mu.Unlock()
		for _, connection := range server.conns {
			_ = connection.Close()
		}
	})
	return server
}

func (s *dbTunnelIntegrationSSHServer) address() (string, int) {
	host, portText, err := net.SplitHostPort(s.listener.Addr().String())
	if err != nil {
		return "", 0
	}
	port, _ := strconv.Atoi(portText)
	return host, port
}

func (s *dbTunnelIntegrationSSHServer) accept() {
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(connection)
	}
}

func (s *dbTunnelIntegrationSSHServer) handle(raw net.Conn) {
	connection, channels, requests, err := gossh.NewServerConn(raw, s.config)
	if err != nil {
		_ = raw.Close()
		return
	}
	id := s.nextID.Add(1)
	s.mu.Lock()
	s.conns[id] = connection
	s.mu.Unlock()
	go gossh.DiscardRequests(requests)
	defer func() {
		s.mu.Lock()
		delete(s.conns, id)
		s.mu.Unlock()
		_ = connection.Close()
	}()
	for channel := range channels {
		if channel.ChannelType() != "direct-tcpip" {
			_ = channel.Reject(gossh.UnknownChannelType, "only direct-tcpip is supported")
			continue
		}
		go s.handleDirect(id, channel)
	}
}

func (s *dbTunnelIntegrationSSHServer) handleDirect(id uint64, newChannel gossh.NewChannel) {
	var payload struct {
		Host       string
		Port       uint32
		OriginHost string
		OriginPort uint32
	}
	if err := gossh.Unmarshal(newChannel.ExtraData(), &payload); err != nil {
		_ = newChannel.Reject(gossh.Prohibited, "invalid direct-tcpip payload")
		return
	}
	address := net.JoinHostPort(payload.Host, strconv.Itoa(int(payload.Port)))
	if address != s.remoteAddress {
		_ = newChannel.Reject(gossh.Prohibited, "unknown remote database address")
		return
	}
	upstream, err := net.DialTimeout("tcp", s.target, 5*time.Second)
	if err != nil {
		_ = newChannel.Reject(gossh.ConnectionFailed, "remote database is unavailable")
		return
	}
	channel, requests, err := newChannel.Accept()
	if err != nil {
		_ = upstream.Close()
		return
	}
	s.mu.Lock()
	s.channels[id]++
	s.mu.Unlock()
	go gossh.DiscardRequests(requests)
	go func() {
		_, _ = io.Copy(channel, upstream)
		_ = channel.CloseWrite()
	}()
	go func() {
		_, _ = io.Copy(upstream, channel)
		_ = upstream.Close()
		_ = channel.Close()
	}()
}

func (s *dbTunnelIntegrationSSHServer) channelCount(id uint64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.channels[id]
}

func newDBTunnelIntegrationManager(t *testing.T, server *dbTunnelIntegrationSSHServer) *session.Manager {
	t.Helper()
	host, port := server.address()
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(ctx context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
			return transportssh.Connect(ctx, transportssh.Config{
				Host: host, Port: port, User: "test",
				Auth:              transportssh.AuthConfig{Method: transportssh.AuthPassword, Password: "secret"},
				HostKeys:          transportssh.NewMemoryHostKeyStore(),
				AutoAcceptUnknown: true,
				ConnectTimeout:    5 * time.Second,
			})
		}),
		ReconnectBackoff: []time.Duration{0},
		IdleTimeout:      -1,
	})
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

type dbTunnelIntegrationPostgres struct {
	config *pgx.ConnConfig
	caFile string
}

func startDBTunnelIntegrationTLSPostgres(t *testing.T) dbTunnelIntegrationPostgres {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker is required for the real TLS PostgreSQL test: %v", err)
	}
	dir, caFile := writeDBTunnelIntegrationTLSFiles(t)
	const password = "db-tunnel-integration-password"
	name := fmt.Sprintf("nexterm-db-tunnel-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	output, err := exec.CommandContext(ctx, "docker", "run", "--detach", "--rm", "--name", name,
		"--publish", "127.0.0.1::5432",
		"--env", "POSTGRES_USER=nexterm",
		"--env", "POSTGRES_PASSWORD="+password,
		"--env", "POSTGRES_DB=nexterm_test",
		"--volume", dir+":/tls",
		"postgres:16", "bash", "-c",
		"chown postgres:postgres /tls/server.key && chmod 600 /tls/server.key && "+
			"exec docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/tls/server.crt -c ssl_key_file=/tls/server.key "+
			"-c ssl_ca_file=/tls/ca.crt -c hba_file=/tls/pg_hba.conf",
	).CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("start TLS PostgreSQL: %v: %s", err, output)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "docker", "rm", "--force", name).Run()
	})
	ctx, cancel = context.WithTimeout(t.Context(), 15*time.Second)
	output, err = exec.CommandContext(ctx, "docker", "port", name, "5432/tcp").CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("inspect TLS PostgreSQL: %v: %s", err, output)
	}
	address := strings.TrimSpace(strings.Split(string(output), "\n")[0])
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("parse TLS PostgreSQL address %q: %v", address, err)
	}
	config, err := pgx.ParseConfig(fmt.Sprintf("postgres://nexterm:%s@%s/nexterm_test?sslmode=disable", password, address))
	if err != nil {
		t.Fatal(err)
	}
	config.Host = host
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	config.Port = uint16(port)
	config.Fallbacks = nil
	config.TLSConfig = dbTunnelIntegrationTLSConfig(t, caFile, dbTunnelIntegrationHost)
	deadline := time.Now().Add(90 * time.Second)
	for {
		connectCtx, cancelConnect := context.WithTimeout(t.Context(), 3*time.Second)
		connection, connectErr := pgx.ConnectConfig(connectCtx, config.Copy())
		cancelConnect()
		if connectErr == nil {
			_ = connection.Close(t.Context())
			break
		}
		err = connectErr
		if time.Now().After(deadline) {
			t.Fatalf("TLS PostgreSQL did not become ready: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return dbTunnelIntegrationPostgres{config: config, caFile: caFile}
}

func dbTunnelIntegrationTLSConfig(t *testing.T, caFile, serverName string) *tls.Config {
	t.Helper()
	pemData, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemData) {
		t.Fatal("invalid test CA")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName, RootCAs: roots}
}

func writeDBTunnelIntegrationTLSFiles(t *testing.T) (string, string) {
	t.Helper()
	return writeDBTunnelIntegrationTLSFilesForHost(t, dbTunnelIntegrationHost)
}

func writeDBTunnelIntegrationTLSFilesForHost(t *testing.T, serverName string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "NexTerm DB Tunnel Test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: serverName},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{serverName},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, blockType string, data []byte, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if blockType != "" {
			data = pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: data})
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	caFile := write("ca.crt", "CERTIFICATE", caDER, 0o644)
	write("server.crt", "CERTIFICATE", serverDER, 0o644)
	write("server.key", "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(serverKey), 0o600)
	hba := "local all all trust\n" +
		"hostssl all all 0.0.0.0/0 md5\nhostssl all all ::/0 md5\n" +
		"host all all 0.0.0.0/0 reject\nhost all all ::/0 reject\n"
	write("pg_hba.conf", "", []byte(hba), 0o644)
	return dir, caFile
}
