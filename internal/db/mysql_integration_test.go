package db

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestMySQL84DockerIntegration(t *testing.T) {
	requireDockerIntegration(t)
	tlsFiles := makeTestTLSFiles(t)
	const password = "m18-integration-password"
	container := startDockerTestContainer(t, "3306",
		"--env", "MYSQL_ROOT_PASSWORD="+password,
		"--env", "MYSQL_ROOT_HOST=%",
		"--env", "MYSQL_DATABASE=nexterm_test",
		"--volume", tlsFiles.dir+":/tls:ro",
		"mysql:8.4",
		"--require-secure-transport=ON",
		"--ssl-ca=/tls/ca.crt",
		"--ssl-cert=/tls/server.crt",
		"--ssl-key=/tls/server.key",
	)
	tlsConfig, err := makeTLSConfig(container.host, tlsFiles.options)
	if err != nil {
		t.Fatalf("TLS config: %v", err)
	}
	config := MySQLConfig{Username: "root", Password: password, Database: "nexterm_test", TLSConfig: tlsConfig}
	waitForMySQL(t, container, config)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, err = connectMySQL(ctx, MySQLConfig{Host: container.host, Port: container.port, Username: "root", Password: password, Database: "nexterm_test"})
	cancel()
	if err == nil {
		t.Fatal("plaintext connection unexpectedly succeeded against TLS-only MySQL")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	_, err = connectMySQL(ctx, MySQLConfig{Host: container.host, Port: container.port, Username: "root", Password: "wrong", Database: "nexterm_test", TLSConfig: tlsConfig})
	cancel()
	if err == nil {
		t.Fatal("wrong MySQL password unexpectedly succeeded")
	}

	service := NewService(nil)
	t.Cleanup(func() { _ = service.Close() })
	connected, err := service.Connect(context.Background(), ConnectArgs{Inline: &InlineConnection{
		Kind: "mysql", Host: container.host, Port: &container.port, Username: "root", Password: password, Database: "nexterm_test", TLS: tlsFiles.options,
	}})
	if err != nil {
		t.Fatalf("connect with verified TLS: %v\n%s", err, container.logs(t))
	}
	connID := connected.ConnID

	result := mustMySQLQuery(t, service, connID, "SELECT VERSION()", 0, 5*time.Second)
	version, ok := result.Rows[0][0].(string)
	if !ok || !strings.HasPrefix(version, "8.4.") {
		t.Fatalf("MySQL version=%q, want 8.4.x", result.Rows[0][0])
	}

	mustMySQLQuery(t, service, connID, `CREATE TABLE m18_types (
		id INT PRIMARY KEY,
		u BIGINT UNSIGNED,
		decv DECIMAL(30,6),
		txt TEXT,
		bin VARBINARY(8),
		nul TEXT,
		created DATE
	)`, 0, 5*time.Second)
	insert := mustMySQLQuery(t, service, connID, `INSERT INTO m18_types VALUES
		(1, 18446744073709551615, 12345678901234567890.012300, 'hello', X'00FF01', NULL, '2026-10-02')`, 0, 5*time.Second)
	if insert.RowsAffected != 1 {
		t.Fatalf("INSERT rowsAffected=%d", insert.RowsAffected)
	}
	result = mustMySQLQuery(t, service, connID, "SELECT id, u, decv, txt, bin, nul, created FROM m18_types", 0, 5*time.Second)
	want := []any{int64(1), "18446744073709551615", "12345678901234567890.012300", "hello", "AP8B", nil, "2026-10-02"}
	if !reflect.DeepEqual(result.Rows[0], want) {
		t.Fatalf("types got  %#v\nwant %#v", result.Rows[0], want)
	}

	result = mustMySQLQuery(t, service, connID, "SELECT id, txt FROM m18_types WHERE 0", 0, 5*time.Second)
	if !reflect.DeepEqual(result.Columns, []string{"id", "txt"}) || len(result.Rows) != 0 {
		t.Fatalf("empty result metadata: %+v", result)
	}
	update := mustMySQLQuery(t, service, connID, "UPDATE m18_types SET txt = 'updated' WHERE id = 1", 0, 5*time.Second)
	if update.RowsAffected != 1 {
		t.Fatalf("UPDATE rowsAffected=%d", update.RowsAffected)
	}

	result = mustMySQLQuery(t, service, connID, "(SELECT 1);", 0, 5*time.Second)
	if len(result.Rows) != 1 || result.Rows[0][0] != int64(1) {
		t.Fatalf("parenthesized SELECT result: %+v", result)
	}
	result = mustMySQLQuery(t, service, connID, "(SELECT 1 UNION SELECT 2);", 0, 5*time.Second)
	if len(result.Rows) != 2 || result.Rows[0][0] != int64(1) || result.Rows[1][0] != int64(2) {
		t.Fatalf("parenthesized UNION result: %+v", result)
	}

	mustMySQLQuery(t, service, connID, `CREATE TABLE m18_bits (
		id INT PRIMARY KEY,
		bit_zero BIT(1),
		bit_one BIT(1),
		bit_ascii BIT(8),
		bit_wide BIT(64)
	)`, 0, 5*time.Second)
	mustMySQLQuery(t, service, connID, `INSERT INTO m18_bits VALUES
		(1, b'0', b'1', b'00110001', b'1111111111111111111111111111111111111111111111111111111111111111')`, 0, 5*time.Second)
	result = mustMySQLQuery(t, service, connID, "SELECT id, bit_zero, bit_one, bit_ascii, bit_wide FROM m18_bits", 0, 5*time.Second)
	want = []any{int64(1), uint64(0), uint64(1), uint64(49), "18446744073709551615"}
	if !reflect.DeepEqual(result.Rows[0], want) {
		t.Fatalf("BIT values got  %#v\nwant %#v", result.Rows[0], want)
	}

	schemas, err := service.Schemas(context.Background(), connID)
	if err != nil || !containsString(schemas, "nexterm_test") {
		t.Fatalf("schemas=%v err=%v", schemas, err)
	}
	tables, err := service.Tables(context.Background(), connID, "")
	if err != nil || !containsString(tables, "m18_types") {
		t.Fatalf("tables=%v err=%v", tables, err)
	}
	description, err := service.Describe(context.Background(), connID, "", "m18_types")
	if err != nil || len(description.Columns) != 7 || len(description.Indexes) == 0 {
		t.Fatalf("description=%+v err=%v", description, err)
	}

	mustMySQLQuery(t, service, connID, `CREATE TABLE m18_functional_indexes (
		id INT PRIMARY KEY,
		txt VARCHAR(255),
		INDEX idx_lower_txt ((LOWER(txt)))
	)`, 0, 5*time.Second)
	description, err = service.Describe(context.Background(), connID, "", "m18_functional_indexes")
	if err != nil || len(description.Columns) != 2 {
		t.Fatalf("functional index description=%+v err=%v", description, err)
	}
	functionalIndexFound := false
	for _, index := range description.Indexes {
		if index.Name == "idx_lower_txt" {
			functionalIndexFound = true
			if index.Column != "" {
				t.Fatalf("functional index column=%q, want empty DTO string", index.Column)
			}
		}
	}
	if !functionalIndexFound {
		t.Fatalf("functional index missing from description: %+v", description.Indexes)
	}

	result, err = service.Query(context.Background(), connID, "SELECT 1; DROP TABLE m18_types", 0, 5*time.Second)
	if err != nil || result.Error == nil {
		t.Fatalf("multi-statement was not rejected: result=%+v err=%v", result, err)
	}
	result = mustMySQLQuery(t, service, connID, "SELECT COUNT(*) FROM m18_types", 0, 5*time.Second)
	if result.Rows[0][0] != int64(1) {
		t.Fatalf("second statement executed: %+v", result)
	}

	_, err = service.Query(context.Background(), connID, "SELECT SLEEP(2)", 0, 50*time.Millisecond)
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeTimeout {
		t.Fatalf("timeout error=%#v", err)
	}
	mustMySQLQuery(t, service, connID, "SELECT 1", 0, 5*time.Second)

	digits := "(SELECT 0 AS n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9)"
	statement := fmt.Sprintf(`SELECT d0.n + d1.n * 10 + d2.n * 100 + d3.n * 1000 AS n
		FROM %s AS d0 CROSS JOIN %s AS d1 CROSS JOIN %s AS d2 CROSS JOIN %s AS d3`, digits, digits, digits, digits)
	result = mustMySQLQuery(t, service, connID, statement, 0, 30*time.Second)
	if len(result.Rows) != MaxRows || !result.Truncated {
		t.Fatalf("truncation rows=%d truncated=%v", len(result.Rows), result.Truncated)
	}

	withService := mysqlThreadsConnected(t, container, config)
	if withService < 2 {
		t.Fatalf("Threads_connected=%d, want the service connection plus the probe", withService)
	}
	if err := service.Disconnect(connID); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if _, err := service.mysqlOf(connID); err == nil {
		t.Fatal("MySQL connection handle survived disconnect")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		after := mysqlThreadsConnected(t, container, config)
		if after < withService {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Threads_connected=%d after disconnect, want < %d", after, withService)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err := service.Disconnect(connID); err != nil {
		t.Fatalf("idempotent disconnect: %v", err)
	}
}

func mysqlThreadsConnected(t *testing.T, container dockerTestContainer, config MySQLConfig) int {
	t.Helper()
	config.Host = container.host
	config.Port = container.port
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, err := connectMySQL(ctx, config)
	if err != nil {
		t.Fatalf("probe connect: %v", err)
	}
	defer connection.Close()
	var name string
	var value int
	if err := connection.db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Threads_connected'").Scan(&name, &value); err != nil {
		t.Fatalf("probe Threads_connected: %v", err)
	}
	return value
}

func mustMySQLQuery(t *testing.T, service *Service, connID, statement string, limit uint64, timeout time.Duration) QueryResult {
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
