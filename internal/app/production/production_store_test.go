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

	_, err = openProductionStore(t.Context(), ProductionConfig{
		DataDir: t.TempDir(),
		DB: store.BackendConfig{
			Backend:        store.BackendPostgres,
			DSN:            "postgres://nexterm@127.0.0.1:1/nexterm?sslmode=disable&connect_timeout=2",
			ConnectTimeout: 3 * time.Second,
		},
	})
	if err == nil {
		t.Fatal("unreachable postgres backend was accepted")
	}
}
