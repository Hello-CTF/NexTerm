package db

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestIPCRegistrationAndWireShapes(t *testing.T) {
	state := &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) {
		return newFakeRows([]string{"id"}, []string{"INT"}, nil), nil
	}}
	database := newFakeSQLDB(t, state)
	host := "localhost"
	service := NewService(AssetResolverFunc(func(context.Context, string) (Asset, error) {
		return Asset{Kind: "mysql", Host: &host}, nil
	}))
	service.newID = func() (string, error) { return "conn-1", nil }
	service.openMySQL = func(_ context.Context, config MySQLConfig) (*mysqlConnection, error) {
		return &mysqlConnection{db: database, database: config.Database}, nil
	}
	dispatcher := ipc.NewDispatcher()
	if err := service.RegisterCommands(dispatcher); err != nil {
		t.Fatalf("register: %v", err)
	}
	registered := map[string]bool{}
	for _, command := range dispatcher.Commands() {
		registered[command] = true
	}
	for _, command := range []string{"db_columns", "db_connect", "db_disconnect", "db_query", "db_schemas", "db_tables", "redis_command", "redis_inspect", "redis_scan", "redis_set_ttl"} {
		if !registered[command] {
			t.Fatalf("missing command %s in %v", command, dispatcher.Commands())
		}
	}

	response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "db_connect", Args: json.RawMessage(`{"args":{"assetId":"mysql-asset"}}`)}, ipc.Environment{})
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
	response = dispatcher.Dispatch(context.Background(), ipc.Request{Command: "db_connect", Args: json.RawMessage(`{"args":{"inline":{"kind":"mysql","host":"localhost"}}}`)}, ipc.Environment{})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("inline connect response=%+v, want bad_param", response)
	}
}

func TestIPCPostgresConnectDispatch(t *testing.T) {
	state := &fakeSQLState{query: func(_ context.Context, statement string) (driver.Rows, error) {
		if strings.Contains(statement, "information_schema.schemata") {
			return newFakeRows([]string{"schema_name"}, nil, [][]driver.Value{{"public"}}), nil
		}
		return newFakeRows([]string{"id"}, []string{"INT8"}, nil), nil
	}}
	database := newFakeSQLDB(t, state)
	host := "localhost"
	service := NewService(AssetResolverFunc(func(context.Context, string) (Asset, error) {
		return Asset{Kind: "postgres", Host: &host}, nil
	}))
	service.newID = func() (string, error) { return "pg-1", nil }
	service.openPostgres = func(_ context.Context, config PostgresConfig) (*postgresConnection, error) {
		if config.Port != 5432 {
			t.Fatalf("port=%d", config.Port)
		}
		return &postgresConnection{db: database}, nil
	}
	dispatcher := ipc.NewDispatcher()
	if err := service.RegisterCommands(dispatcher); err != nil {
		t.Fatalf("register: %v", err)
	}
	response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "db_connect", Args: json.RawMessage(`{"args":{"assetId":"postgres-asset"}}`)}, ipc.Environment{})
	if !response.OK || string(response.Data) != `{"connId":"pg-1"}` {
		t.Fatalf("connect response=%+v data=%s error=%v", response, response.Data, response.Error)
	}
	response = dispatcher.Dispatch(context.Background(), ipc.Request{Command: "db_schemas", Args: json.RawMessage(`{"connId":"pg-1"}`)}, ipc.Environment{})
	if !response.OK || string(response.Data) != `["public"]` {
		t.Fatalf("schemas response=%+v data=%s", response, response.Data)
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

func callDBHTTP(t *testing.T, client *http.Client, url, token, command string, args any) (int, []byte, ipc.Response) {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"cmd": command, "args": args})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ipc.Response
	if response.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("decode %s response: %v; body=%s", command, err, body)
		}
	}
	return response.StatusCode, body, decoded
}

func connectDBHTTP(t *testing.T, client *http.Client, url, token, kind string) string {
	t.Helper()
	_, body, response := callDBHTTP(t, client, url, token, "db_connect", map[string]any{
		"args": map[string]any{"assetId": kind},
	})
	if !response.OK {
		t.Fatalf("%s connect failed: body=%s error=%+v", kind, body, response.Error)
	}
	var result ConnectResult
	if err := json.Unmarshal(response.Data, &result); err != nil || result.ConnID == "" {
		t.Fatalf("%s connect data=%s err=%v", kind, response.Data, err)
	}
	return result.ConnID
}

func requireDBHTTPSuccess(t *testing.T, client *http.Client, url, token, command string, args any) []byte {
	t.Helper()
	status, body, response := callDBHTTP(t, client, url, token, command, args)
	if status != http.StatusOK || !response.OK {
		t.Fatalf("%s status=%d body=%s error=%+v", command, status, body, response.Error)
	}
	return body
}

func TestDBConnectionOwnershipOverHTTP(t *testing.T) {
	const (
		tableMarker    = "owner_table_marker"
		dataMarker     = "owner_data_marker"
		columnMarker   = "owner_column_marker"
		redisMarker    = "owner_redis_marker"
		pgTableMarker  = "postgres_table_marker"
		pgDataMarker   = "postgres_data_marker"
		pgColumnMarker = "postgres_column_marker"
		pgIndexMarker  = "postgres_index_marker"
	)
	state := &fakeSQLState{query: func(_ context.Context, statement string) (driver.Rows, error) {
		switch {
		case strings.Contains(statement, "information_schema.schemata"):
			return newFakeRows([]string{"schema_name"}, nil, [][]driver.Value{{"app"}}), nil
		case strings.Contains(statement, "information_schema.tables"):
			return newFakeRows([]string{"table_name"}, nil, [][]driver.Value{{tableMarker}}), nil
		case strings.Contains(statement, "information_schema.columns"):
			return newFakeRows([]string{"column_name", "data_type", "is_nullable", "column_key", "column_default", "extra"}, nil, [][]driver.Value{{columnMarker, "text", "NO", "", nil, ""}}), nil
		case strings.Contains(statement, "information_schema.statistics"):
			return newFakeRows([]string{"index_name", "non_unique", "seq_in_index", "column_name"}, nil, [][]driver.Value{{"owner_index", 0, 1, columnMarker}}), nil
		default:
			return newFakeRows([]string{"value"}, nil, [][]driver.Value{{dataMarker}}), nil
		}
	}}
	pgState := &fakeSQLState{query: func(_ context.Context, statement string) (driver.Rows, error) {
		switch {
		case strings.Contains(statement, "information_schema.schemata"):
			return newFakeRows([]string{"schema_name"}, nil, [][]driver.Value{{"public"}}), nil
		case strings.Contains(statement, "information_schema.tables"):
			return newFakeRows([]string{"table_name"}, nil, [][]driver.Value{{pgTableMarker}}), nil
		case strings.Contains(statement, "information_schema.columns"):
			return newFakeRows([]string{"column_name", "data_type", "is_nullable", "column_default", "is_identity"}, nil, [][]driver.Value{{pgColumnMarker, "text", "NO", nil, ""}}), nil
		case strings.Contains(statement, "pg_constraint"):
			return newFakeRows([]string{"attname"}, nil, [][]driver.Value{{pgColumnMarker}}), nil
		case strings.Contains(statement, "pg_index"):
			return newFakeRows([]string{"relname", "indisunique", "ord", "attname"}, nil, [][]driver.Value{{pgIndexMarker, true, 1, pgColumnMarker}}), nil
		default:
			return newFakeRows([]string{"value"}, nil, [][]driver.Value{{pgDataMarker}}), nil
		}
	}}
	redis := &fakeRedis{
		keyType: "string", stringValue: redisMarker, ttl: -2,
		scanKeys: []string{"owner_redis_key"},
		doFunc:   func(...any) (any, error) { return int64(1), nil },
	}
	service := NewService(AssetResolverFunc(func(_ context.Context, id string) (Asset, error) {
		host := "db.internal"
		database := "app"
		if id == "redis" {
			database = "0"
		}
		return Asset{Kind: id, Host: &host, OptionsJSON: `{"database":"` + database + `"}`}, nil
	}))
	service.openMySQL = func(_ context.Context, config MySQLConfig) (*mysqlConnection, error) {
		return &mysqlConnection{db: newFakeSQLDB(t, state), database: config.Database}, nil
	}
	service.openPostgres = func(context.Context, PostgresConfig) (*postgresConnection, error) {
		return &postgresConnection{db: newFakeSQLDB(t, pgState)}, nil
	}
	service.openRedis = func(context.Context, RedisConfig) (*redisConnection, error) {
		return &redisConnection{client: redis, console: redis}, nil
	}
	t.Cleanup(func() { _ = service.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	rpc := ipc.NewRPCHandler(dispatcher, ipc.Environment{})

	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	accounts := account.New(storage.GORM())
	newUser := func(username string) *account.User {
		t.Helper()
		user, err := accounts.CreateUser(context.Background(), username, username, "password-"+username)
		if err != nil {
			t.Fatal(err)
		}
		return user
	}
	newSession := func(user *account.User) string {
		t.Helper()
		token, _, err := accounts.IssueSession(context.Background(), user.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	aliceUser := newUser("alice")
	bobUser := newUser("bob")
	aliceToken := newSession(aliceUser)
	alicePeerToken := newSession(aliceUser)
	bobToken := newSession(bobUser)

	accountServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		identity, err := accounts.ValidateSession(r.Context(), token)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		rpc.ServeHTTP(w, r.WithContext(ipc.WithUserID(r.Context(), identity.UserID)))
	}))
	t.Cleanup(accountServer.Close)
	openServer := httptest.NewServer(rpc)
	t.Cleanup(openServer.Close)

	alice := accountServer.Client()
	alicePeer := &http.Client{}
	bob := &http.Client{}
	mysqlID := connectDBHTTP(t, alice, accountServer.URL, aliceToken, "mysql")
	postgresID := connectDBHTTP(t, alice, accountServer.URL, aliceToken, "postgres")
	redisID := connectDBHTTP(t, alice, accountServer.URL, aliceToken, "redis")

	ownerCalls := []struct {
		command string
		args    any
	}{
		{command: "db_schemas", args: map[string]any{"connId": mysqlID}},
		{command: "db_tables", args: map[string]any{"connId": mysqlID, "schema": "app"}},
		{command: "db_columns", args: map[string]any{"connId": mysqlID, "schema": "app", "table": tableMarker}},
		{command: "db_query", args: map[string]any{"connId": mysqlID, "sql": "SELECT owner_data"}},
		{command: "redis_scan", args: map[string]any{"connId": redisID}},
		{command: "redis_inspect", args: map[string]any{"connId": redisID, "key": "owner"}},
		{command: "redis_command", args: map[string]any{"connId": redisID, "args": []string{"SET", "owner", "updated"}}},
		{command: "redis_set_ttl", args: map[string]any{"connId": redisID, "key": "owner", "seconds": 30}},
	}
	for _, call := range ownerCalls {
		t.Run("same-account-peer/"+call.command, func(t *testing.T) {
			body := requireDBHTTPSuccess(t, alicePeer, accountServer.URL, alicePeerToken, call.command, call.args)
			if call.command == "db_tables" && !strings.Contains(string(body), tableMarker) {
				t.Fatalf("table data missing: %s", body)
			}
			if call.command == "db_query" && !strings.Contains(string(body), dataMarker) {
				t.Fatalf("query data missing: %s", body)
			}
		})
	}

	postgresCalls := []struct {
		command string
		args    any
	}{
		{command: "db_schemas", args: map[string]any{"connId": postgresID}},
		{command: "db_tables", args: map[string]any{"connId": postgresID, "schema": "public"}},
		{command: "db_columns", args: map[string]any{"connId": postgresID, "schema": "public", "table": pgTableMarker}},
		{command: "db_query", args: map[string]any{"connId": postgresID, "sql": "SELECT postgres_data"}},
	}
	for _, call := range postgresCalls {
		t.Run("postgres-same-account-peer/"+call.command, func(t *testing.T) {
			body := requireDBHTTPSuccess(t, alicePeer, accountServer.URL, alicePeerToken, call.command, call.args)
			switch call.command {
			case "db_tables":
				if !strings.Contains(string(body), pgTableMarker) {
					t.Fatalf("PostgreSQL table missing: %s", body)
				}
			case "db_columns":
				if !strings.Contains(string(body), pgColumnMarker) || !strings.Contains(string(body), pgIndexMarker) {
					t.Fatalf("PostgreSQL describe missing: %s", body)
				}
			case "db_query":
				if !strings.Contains(string(body), pgDataMarker) {
					t.Fatalf("PostgreSQL query data missing: %s", body)
				}
			}
		})
	}

	redisCallsBeforeForeign := len(redis.doCalls)
	for _, call := range ownerCalls {
		t.Run("foreign/"+call.command, func(t *testing.T) {
			status, body, response := callDBHTTP(t, bob, accountServer.URL, bobToken, call.command, call.args)
			if status != http.StatusOK || response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
				t.Fatalf("foreign access was not rejected: status=%d body=%s", status, body)
			}
			for _, marker := range []string{tableMarker, dataMarker, columnMarker, redisMarker} {
				if strings.Contains(string(body), marker) {
					t.Fatalf("foreign response leaked %s: %s", marker, body)
				}
			}
		})
	}
	for _, call := range postgresCalls {
		t.Run("postgres-foreign/"+call.command, func(t *testing.T) {
			status, body, response := callDBHTTP(t, bob, accountServer.URL, bobToken, call.command, call.args)
			if status != http.StatusOK || response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
				t.Fatalf("foreign PostgreSQL access was not rejected: status=%d body=%s", status, body)
			}
			for _, marker := range []string{pgTableMarker, pgDataMarker, pgColumnMarker, pgIndexMarker} {
				if strings.Contains(string(body), marker) {
					t.Fatalf("foreign PostgreSQL response leaked %s: %s", marker, body)
				}
			}
		})
	}
	for _, connID := range []string{mysqlID, postgresID, redisID} {
		status, body, response := callDBHTTP(t, bob, accountServer.URL, bobToken, "db_disconnect", map[string]any{"connId": connID})
		if status != http.StatusOK || response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
			t.Fatalf("foreign disconnect %s was not rejected: status=%d body=%s", connID, status, body)
		}
	}
	if len(redis.doCalls) != redisCallsBeforeForeign {
		t.Fatalf("foreign Redis operations changed state: before=%d after=%d calls=%v", redisCallsBeforeForeign, len(redis.doCalls), redis.doCalls)
	}
	requireDBHTTPSuccess(t, alice, accountServer.URL, aliceToken, "db_query", map[string]any{"connId": mysqlID, "sql": "SELECT owner_data"})
	requireDBHTTPSuccess(t, alice, accountServer.URL, aliceToken, "db_query", map[string]any{"connId": postgresID, "sql": "SELECT postgres_data"})
	requireDBHTTPSuccess(t, alice, accountServer.URL, aliceToken, "redis_inspect", map[string]any{"connId": redisID, "key": "owner"})

	t.Run("unknown-request-context", func(t *testing.T) {
		_, err := service.Tables(context.Background(), mysqlID, "app")
		var ipcErr *ipc.Error
		if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeForbidden {
			t.Fatalf("unknown context error=%#v, want forbidden", err)
		}
	})
	t.Run("open-can-read-owned", func(t *testing.T) {
		requireDBHTTPSuccess(t, openServer.Client(), openServer.URL, "", "db_tables", map[string]any{"connId": mysqlID, "schema": "app"})
	})
	openMySQLID := connectDBHTTP(t, openServer.Client(), openServer.URL, "", "mysql")
	t.Run("open-ownerless", func(t *testing.T) {
		requireDBHTTPSuccess(t, openServer.Client(), openServer.URL, "", "db_tables", map[string]any{"connId": openMySQLID, "schema": "app"})
		if _, err := service.Tables(context.Background(), openMySQLID, "app"); err != nil {
			t.Fatalf("ownerless direct access failed: %v", err)
		}
	})
	t.Run("account-cannot-read-ownerless", func(t *testing.T) {
		status, body, response := callDBHTTP(t, alice, accountServer.URL, aliceToken, "db_tables", map[string]any{"connId": openMySQLID, "schema": "app"})
		if status != http.StatusOK || response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
			t.Fatalf("ownerless connection was exposed to account: status=%d body=%s", status, body)
		}
		if strings.Contains(string(body), tableMarker) {
			t.Fatalf("ownerless table leaked to account: %s", body)
		}
	})
}
