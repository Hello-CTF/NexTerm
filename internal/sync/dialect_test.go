package sync

import (
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestSyncDialectTokens(t *testing.T) {
	if scalarMax(store.BackendSQLite) != "max" || scalarMax(store.BackendPostgres) != "GREATEST" {
		t.Fatalf("scalarMax = %q / %q", scalarMax(store.BackendSQLite), scalarMax(store.BackendPostgres))
	}
	if forUpdate(store.BackendSQLite) != "" || forUpdate(store.BackendPostgres) != " FOR UPDATE" {
		t.Fatalf("forUpdate = %q / %q", forUpdate(store.BackendSQLite), forUpdate(store.BackendPostgres))
	}
}
