package db

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestIPCRegistrationAndWireShapes(t *testing.T) {
	state := &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) {
		return newFakeRows([]string{"id"}, []string{"INT"}, nil), nil
	}}
	database := newFakeSQLDB(t, state)
	service := NewService(nil)
	service.newID = func() (string, error) { return "conn-1", nil }
	service.openMySQL = func(_ context.Context, config MySQLConfig) (*mysqlConnection, error) {
		return &mysqlConnection{db: database, database: config.Database}, nil
	}
	dispatcher := ipc.NewDispatcher()
	if err := service.RegisterCommands(dispatcher); err != nil {
		t.Fatalf("register: %v", err)
	}
	wantCommands := []string{"db_columns", "db_connect", "db_disconnect", "db_query", "db_schemas", "db_tables", "redis_command", "redis_inspect", "redis_scan", "redis_set_ttl"}
	if !reflect.DeepEqual(dispatcher.Commands(), wantCommands) {
		t.Fatalf("commands=%v", dispatcher.Commands())
	}

	response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "db_connect", Args: json.RawMessage(`{"args":{"inline":{"kind":"mysql","host":"localhost"}}}`)}, ipc.Environment{})
	if !response.OK || string(response.Data) != `{"connId":"conn-1"}` {
		t.Fatalf("connect response=%+v data=%s error=%v", response, response.Data, response.Error)
	}
	response = dispatcher.Dispatch(context.Background(), ipc.Request{Command: "db_query", Args: json.RawMessage(`{"connId":"conn-1","sql":"SELECT id FROM empty"}`)}, ipc.Environment{})
	var query QueryResult
	if !response.OK || json.Unmarshal(response.Data, &query) != nil || !reflect.DeepEqual(query.Columns, []string{"id"}) || query.Rows == nil || query.Error != nil {
		t.Fatalf("query response=%+v data=%s", response, response.Data)
	}
	response = dispatcher.Dispatch(context.Background(), ipc.Request{Command: "db_disconnect", Args: json.RawMessage(`{"connId":"conn-1"}`)}, ipc.Environment{})
	if !response.OK || string(response.Data) != "null" {
		t.Fatalf("disconnect response=%+v data=%s", response, response.Data)
	}
	response = dispatcher.Dispatch(context.Background(), ipc.Request{Command: "db_schemas", Args: json.RawMessage(`{"connId":"conn-1"}`)}, ipc.Environment{})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound || response.Error.Message == "" {
		t.Fatalf("error response=%+v", response)
	}
}

func TestIPCRedisScanIsTuple(t *testing.T) {
	service := NewService(nil)
	service.conns["redis"] = &redisConnection{client: &fakeRedis{scanCursor: 3, scanKeys: []string{"a"}}, console: &fakeRedis{}}
	dispatcher := ipc.NewDispatcher()
	if err := service.RegisterCommands(dispatcher); err != nil {
		t.Fatalf("register: %v", err)
	}
	response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "redis_scan", Args: json.RawMessage(`{"connId":"redis"}`)}, ipc.Environment{})
	if !response.OK || string(response.Data) != `[3,["a"]]` {
		t.Fatalf("response=%+v data=%s", response, response.Data)
	}
}
