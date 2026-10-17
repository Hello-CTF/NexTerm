package production

import (
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestOpenProductionStoreBackendSelection(t *testing.T) {
	sqlite, err := openProductionStore(t.Context(), ProductionConfig{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if sqlite.Backend() != store.BackendSQLite {
		t.Fatalf("default backend = %q", sqlite.Backend())
	}
	if err := sqlite.Close(); err != nil {
		t.Fatal(err)
	}

	for _, backend := range []store.Backend{store.BackendPostgres, store.BackendMySQL, store.BackendMariaDB} {
		t.Run(string(backend), func(t *testing.T) {
			dsn := "nexterm@tcp(127.0.0.1:1)/nexterm?timeout=2s"
			if backend == store.BackendPostgres {
				dsn = "postgres://nexterm@127.0.0.1:1/nexterm?sslmode=disable&connect_timeout=2"
			}
			_, err := openProductionStore(t.Context(), ProductionConfig{
				DataDir: t.TempDir(),
				DB:      store.BackendConfig{Backend: backend, DSN: dsn, ConnectTimeout: 3 * time.Second},
			})
			if err == nil {
				t.Fatal("unreachable remote backend was accepted")
			}
		})
	}
}
