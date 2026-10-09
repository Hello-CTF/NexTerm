package production

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestProductionDatabaseModuleRegistersLifecycleCommands(t *testing.T) {
	modules := productionModules(ProductionServices{Database: db.NewService(nil)})
	var database *Module
	for i := range modules {
		if modules[i].Name == "database" {
			database = &modules[i]
		}
	}
	if database == nil {
		t.Fatal("database module is not registered in production wiring")
	}
	if database.Component == nil {
		t.Fatal("database module has no lifecycle component")
	}
	dispatcher := ipc.NewDispatcher()
	if err := database.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"db_connect", "db_disconnect", "db_schemas", "db_tables", "db_columns", "db_query",
		"redis_scan", "redis_inspect", "redis_command", "redis_set_ttl",
	} {
		if !slices.Contains(dispatcher.Commands(), command) {
			t.Fatalf("missing %s in %v", command, dispatcher.Commands())
		}
	}

	response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "db_disconnect", Args: json.RawMessage(`{"connId":"missing"}`)}, ipc.Environment{})
	if !response.OK || string(response.Data) != "null" {
		t.Fatalf("idempotent disconnect response=%+v data=%s", response, response.Data)
	}
	if err := database.Component.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
