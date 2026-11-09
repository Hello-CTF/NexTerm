package db

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestMariaDB114DockerIntegration(t *testing.T) {
	requireDockerIntegration(t)
	const password = "mariadb-integration-password"
	container := startDockerTestContainer(t, "3306",
		"--env", "MARIADB_ROOT_PASSWORD="+password,
		"--env", "MARIADB_ROOT_HOST=%",
		"--env", "MARIADB_DATABASE=nexterm_test",
		"mariadb:11.4@sha256:250e1609e93c0e6aecf64bfbab2d84d5e89d2481540fe77447ff6c855c3b1873",
	)
	config := MySQLConfig{Username: "root", Password: password, Database: "nexterm_test"}
	waitForMySQL(t, container, config)
	config.Host = container.host
	config.Port = container.port
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	connection, err := connectMySQL(ctx, config)
	cancel()
	if err != nil {
		t.Fatalf("connect: %v\n%s", err, container.logs(t))
	}
	t.Cleanup(func() { _ = connection.Close() })

	query := func(statement string) QueryResult {
		t.Helper()
		result, err := queryMySQL(context.Background(), connection.db, statement, 0, 5*time.Second)
		if err != nil || result.Error != nil {
			t.Fatalf("query %q: result=%+v err=%v", statement, result, err)
		}
		return result
	}
	query("CREATE TABLE items (id INT PRIMARY KEY AUTO_INCREMENT, name TEXT)")

	inserted := query("-- add one\nINSERT INTO items (name) VALUES ('a returning b') RETURNING id, name")
	if !reflect.DeepEqual(inserted.Columns, []string{"id", "name"}) || !reflect.DeepEqual(inserted.Rows, [][]any{{int64(1), "a returning b"}}) {
		t.Fatalf("INSERT RETURNING result: %+v", inserted)
	}
	plain := query("INSERT INTO items (name) VALUES ('returning')")
	if plain.RowsAffected != 1 || len(plain.Columns) != 0 {
		t.Fatalf("plain INSERT result: %+v", plain)
	}
	deleted := query("DELETE FROM items WHERE id = 1 RETURNING name")
	if !reflect.DeepEqual(deleted.Rows, [][]any{{"a returning b"}}) {
		t.Fatalf("DELETE RETURNING result: %+v", deleted)
	}
}
