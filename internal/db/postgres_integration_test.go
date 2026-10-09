package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestPostgres16DockerIntegration(t *testing.T) {
	requireDockerIntegration(t)
	tlsFiles := makeTestTLSFiles(t)
	writePostgresHBA(t, tlsFiles.dir)
	const password = "pg16-integration-password"
	container := startDockerTestContainer(t, "5432",
		"--env", "POSTGRES_PASSWORD="+password,
		"--env", "POSTGRES_DB=nexterm_test",
		"--volume", tlsFiles.dir+":/tls",
		"postgres:16",
		"bash", "-c", "chown postgres:postgres /tls/server.key && chmod 600 /tls/server.key && "+
			"exec docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/tls/server.crt -c ssl_key_file=/tls/server.key "+
			"-c ssl_ca_file=/tls/ca.crt -c hba_file=/tls/pg_hba.conf",
	)
	config := PostgresConfig{Username: "postgres", Password: password, Database: "nexterm_test"}
	waitForPostgres(t, container, config)
	tlsConfig, err := makeTLSConfig(container.host, tlsFiles.options)
	if err != nil {
		t.Fatalf("TLS config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, err = connectPostgres(ctx, PostgresConfig{Host: container.host, Port: container.port, Username: "postgres", Password: password, Database: "nexterm_test"})
	cancel()
	if err == nil {
		t.Fatal("plaintext connection unexpectedly succeeded against hostssl-only PostgreSQL")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	_, err = connectPostgres(ctx, PostgresConfig{Host: container.host, Port: container.port, Username: "postgres", Password: "wrong", Database: "nexterm_test", TLSConfig: tlsConfig})
	cancel()
	if err == nil {
		t.Fatal("wrong PostgreSQL password unexpectedly succeeded")
	}

	service := NewService(nil)
	t.Cleanup(func() { _ = service.Close() })
	connected, err := service.Connect(context.Background(), ConnectArgs{Inline: &InlineConnection{
		Kind: "postgres", Host: container.host, Port: &container.port, Username: "postgres", Password: password, Database: "nexterm_test", TLS: tlsFiles.options,
	}})
	if err != nil {
		t.Fatalf("connect with verified TLS: %v\n%s", err, container.logs(t))
	}
	connID := connected.ConnID

	result := mustPostgresQuery(t, service, connID, "SELECT VERSION()", 0, 5*time.Second)
	version, ok := result.Rows[0][0].(string)
	if !ok || !strings.Contains(version, "PostgreSQL 16.") {
		t.Fatalf("PostgreSQL version=%q", result.Rows[0][0])
	}

	mustPostgresQuery(t, service, connID, `CREATE TABLE p16_types (
		id INT8 PRIMARY KEY,
		big INT8,
		decv NUMERIC(30,6),
		txt TEXT,
		bin BYTEA,
		nul TEXT,
		flag BOOL,
		ratio FLOAT8,
		weird FLOAT8,
		created DATE,
		payload JSONB
	)`, 0, 5*time.Second)
	insert := mustPostgresQuery(t, service, connID, `INSERT INTO p16_types VALUES
		(1, 9007199254740993, 12345678901234567890.012300, 'hello', '\x00FF01', NULL, true, 1.25, 'NaN', '2026-10-02', '{"a":1}')`, 0, 5*time.Second)
	if insert.RowsAffected != 1 {
		t.Fatalf("INSERT rowsAffected=%d", insert.RowsAffected)
	}
	result = mustPostgresQuery(t, service, connID, "SELECT id, big, decv, txt, bin, nul, flag, ratio, weird, created, payload FROM p16_types", 0, 5*time.Second)
	want := []any{int64(1), "9007199254740993", "12345678901234567890.012300", "hello", "AP8B", nil, true, 1.25, "NaN", "2026-10-02T00:00:00Z", `{"a":1}`}
	if !reflect.DeepEqual(result.Rows[0], want) {
		t.Fatalf("types got  %#v\nwant %#v", result.Rows[0], want)
	}

	result = mustPostgresQuery(t, service, connID, "SELECT id, txt FROM p16_types WHERE 0", 0, 5*time.Second)
	if !reflect.DeepEqual(result.Columns, []string{"id", "txt"}) || len(result.Rows) != 0 {
		t.Fatalf("empty result metadata: %+v", result)
	}
	update := mustPostgresQuery(t, service, connID, "UPDATE p16_types SET txt = 'updated' WHERE id = 1", 0, 5*time.Second)
	if update.RowsAffected != 1 {
		t.Fatalf("UPDATE rowsAffected=%d", update.RowsAffected)
	}

	mustPostgresQuery(t, service, connID, "CREATE VIEW p16_types_view AS SELECT id, txt FROM p16_types", 0, 5*time.Second)

	schemas, err := service.Schemas(context.Background(), connID)
	if err != nil || !containsString(schemas, "public") {
		t.Fatalf("schemas=%v err=%v", schemas, err)
	}
	tables, err := service.Tables(context.Background(), connID, "public")
	if err != nil || !containsString(tables, "p16_types") || !containsString(tables, "p16_types_view") {
		t.Fatalf("tables=%v err=%v", tables, err)
	}
	description, err := service.Describe(context.Background(), connID, "public", "p16_types")
	if err != nil || len(description.Columns) != 11 {
		t.Fatalf("description=%+v err=%v", description, err)
	}
	if description.Columns[0].Key != "PRI" {
		t.Fatalf("primary key column=%+v", description.Columns[0])
	}
	primaryIndexFound := false
	for _, index := range description.Indexes {
		if index.Name == "p16_types_pkey" && index.Unique && index.Column == "id" {
			primaryIndexFound = true
		}
	}
	if !primaryIndexFound {
		t.Fatalf("primary index missing from description: %+v", description.Indexes)
	}

	result, err = service.Query(context.Background(), connID, "SELECT 1; DROP TABLE p16_types", 0, 5*time.Second)
	if err != nil || result.Error == nil {
		t.Fatalf("multi-statement was not rejected: result=%+v err=%v", result, err)
	}
	result = mustPostgresQuery(t, service, connID, "SELECT COUNT(*) FROM p16_types", 0, 5*time.Second)
	if result.Rows[0][0] != int64(1) {
		t.Fatalf("second statement executed: %+v", result)
	}

	_, err = service.Query(context.Background(), connID, "SELECT pg_sleep(2)", 0, 50*time.Millisecond)
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeTimeout {
		t.Fatalf("timeout error=%#v", err)
	}
	mustPostgresQuery(t, service, connID, "SELECT 1", 0, 5*time.Second)

	result = mustPostgresQuery(t, service, connID, "SELECT g FROM generate_series(1, 6001) AS g", 0, 30*time.Second)
	if len(result.Rows) != MaxRows || !result.Truncated {
		t.Fatalf("truncation rows=%d truncated=%v", len(result.Rows), result.Truncated)
	}

	withService := postgresBackendsConnected(t, container, config)
	if withService < 2 {
		t.Fatalf("backends=%d, want the service connection plus the probe", withService)
	}
	if err := service.Disconnect(connID); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if _, err := service.postgresOf(connID); err == nil {
		t.Fatal("PostgreSQL connection handle survived disconnect")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		after := postgresBackendsConnected(t, container, config)
		if after < withService {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("backends=%d after disconnect, want < %d", after, withService)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err := service.Disconnect(connID); err != nil {
		t.Fatalf("idempotent disconnect: %v", err)
	}
}

func writePostgresHBA(t *testing.T, dir string) {
	t.Helper()
	hba := `local   all             all                                     trust
hostssl all             all             0.0.0.0/0               md5
hostssl all             all             ::/0                    md5
host    all             all             0.0.0.0/0               reject
host    all             all             ::/0                  reject
`
	if err := os.WriteFile(filepath.Join(dir, "pg_hba.conf"), []byte(hba), 0o644); err != nil {
		t.Fatalf("write pg_hba.conf: %v", err)
	}
}

func waitForPostgres(t *testing.T, container dockerTestContainer, config PostgresConfig) {
	t.Helper()
	config.Host = container.host
	config.Port = container.port
	deadline := time.Now().Add(120 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		connection, err := connectPostgres(ctx, config)
		cancel()
		if err == nil {
			_ = connection.Close()
			return
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("PostgreSQL did not become ready: %v\n%s", lastErr, container.logs(t))
}

func postgresBackendsConnected(t *testing.T, container dockerTestContainer, config PostgresConfig) int {
	t.Helper()
	config.Host = container.host
	config.Port = container.port
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, err := connectPostgres(ctx, config)
	if err != nil {
		t.Fatalf("probe connect: %v", err)
	}
	defer connection.Close()
	var count int
	if err := connection.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pg_stat_activity WHERE datname = 'nexterm_test'").Scan(&count); err != nil {
		t.Fatalf("probe backends: %v", err)
	}
	return count
}

func mustPostgresQuery(t *testing.T, service *Service, connID, statement string, limit uint64, timeout time.Duration) QueryResult {
	t.Helper()
	result, err := service.Query(context.Background(), connID, statement, limit, timeout)
	if err != nil {
		t.Fatalf("query %q: %v", statement, err)
	}
	if result.Error != nil {
		t.Fatalf("query %q: %s", statement, *result.Error)
	}
	return result
}
