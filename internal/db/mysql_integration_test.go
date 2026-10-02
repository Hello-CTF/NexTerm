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
