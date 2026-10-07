package server

import (
	"encoding/json"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestHealthzReportsDatabaseMetadata(t *testing.T) {
	db, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	config := testConfig(t, true)
	config.DB = db
	_, httpServer := newTestHTTP(t, config)

	response, err := httpServer.Client().Get(httpServer.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var health Health
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health.DB == nil {
		t.Fatal("healthz omits db metadata")
	}
	if health.DB.Backend != string(store.BackendSQLite) || !health.DB.PingOK || health.DB.Pool == nil {
		t.Fatalf("db health = %+v", health.DB)
	}
	if health.DB.Pool.OpenConnections < 1 || health.DB.Pool.Idle < 1 {
		t.Fatalf("db pool = %+v", health.DB.Pool)
	}
}
