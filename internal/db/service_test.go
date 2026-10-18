package db

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/schema"
)

func TestInlineConnectDisconnectAndTLS(t *testing.T) {
	service := NewService(nil)
	service.newID = func() (string, error) { return "mysql-id", nil }
	database := newFakeSQLDB(t, &fakeSQLState{})
	var captured MySQLConfig
	service.openMySQL = func(_ context.Context, config MySQLConfig) (*mysqlConnection, error) {
		captured = config
		return &mysqlConnection{db: database, database: config.Database}, nil
	}
	port := uint16(3307)
	result, err := service.Connect(context.Background(), ConnectArgs{Inline: &InlineConnection{
		Kind: "mysql", Host: "db.internal", Port: &port, Username: "user", Password: "secret", Database: "app",
		TLS: &TLSOptions{Mode: "skip-verify"},
	}})
	if err != nil || result.ConnID != "mysql-id" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if captured.Host != "db.internal" || captured.Port != 3307 || captured.Username != "user" || captured.Password != "secret" || captured.Database != "app" || captured.TLSConfig == nil || !captured.TLSConfig.InsecureSkipVerify {
		t.Fatalf("captured config: %+v", captured)
	}
	if err := service.Disconnect(context.Background(), result.ConnID); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if err := service.Disconnect(context.Background(), result.ConnID); err != nil {
		t.Fatalf("idempotent disconnect: %v", err)
	}
	_, err = service.mysqlOf(context.Background(), result.ConnID)
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeNotFound {
		t.Fatalf("connection survived disconnect: %#v", err)
	}
}

func TestConnectClosesNewDatabaseHandleWhenAccountSessionWasRevoked(t *testing.T) {
	service := NewService(nil)
	service.newID = func() (string, error) { return "late-db", nil }
	closed := false
	service.openRedis = func(context.Context, RedisConfig) (*redisConnection, error) {
		return &redisConnection{closeFn: func() error { closed = true; return nil }}, nil
	}
	ctx := ipc.WithUserID(t.Context(), "account-a")
	ctx = ipc.WithAccountSessionCheck(ctx, func(context.Context) error {
		return ipc.NewError(ipc.CodeForbidden, "会话已吊销")
	})
	_, err := service.Connect(ctx, ConnectArgs{Inline: &InlineConnection{Kind: "redis", Host: "db.example"}})
	var accessErr *ipc.Error
	if !errors.As(err, &accessErr) || accessErr.Code != ipc.CodeForbidden {
		t.Fatalf("late connect error = %v; want revoked account session", err)
	}
	if !closed {
		t.Fatal("new database handle remained open after account revocation")
	}
	if _, err := service.connOf(ctx, "late-db"); err == nil {
		t.Fatal("revoked account database handle entered the registry")
	}
}

func TestRevokedLoginSessionClosesOnlyItsDatabaseConnections(t *testing.T) {
	service := NewService(nil)
	service.newID = func() (string, error) { return "shared-db", nil }
	closed := false
	service.openRedis = func(context.Context, RedisConfig) (*redisConnection, error) {
		return &redisConnection{closeFn: func() error { closed = true; return nil }}, nil
	}
	owner := ipc.WithUserID(t.Context(), "account-a")
	first := ipc.WithAccountSessionID(owner, "login-a")
	second := ipc.WithAccountSessionID(owner, "login-b")
	result, err := service.Connect(first, ConnectArgs{Inline: &InlineConnection{Kind: "redis", Host: "db.example"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.connOf(second, result.ConnID); err != nil {
		t.Fatalf("second login could not use the shared connection: %v", err)
	}
	if err := service.CloseAccountSession("login-a"); err != nil || closed {
		t.Fatalf("revoking first login closed a connection still used by another login: err=%v closed=%v", err, closed)
	}
	if _, err := service.connOf(second, result.ConnID); err != nil {
		t.Fatalf("second login lost the shared connection: %v", err)
	}
	if err := service.CloseAccountSession("login-b"); err != nil || !closed {
		t.Fatalf("last login revocation did not close its connection: err=%v closed=%v", err, closed)
	}
}

func TestRevokedLoginSessionRejectsConnectionRegisteredAfterLogout(t *testing.T) {
	service := NewService(nil)
	service.newID = func() (string, error) { return "late-db", nil }
	started := make(chan struct{})
	release := make(chan struct{})
	closed := make(chan struct{})
	service.openRedis = func(context.Context, RedisConfig) (*redisConnection, error) {
		close(started)
		<-release
		return &redisConnection{closeFn: func() error { close(closed); return nil }}, nil
	}
	ctx := ipc.WithAccountSessionID(ipc.WithUserID(t.Context(), "account-a"), "login-a")
	result := make(chan error, 1)
	go func() {
		_, err := service.Connect(ctx, ConnectArgs{Inline: &InlineConnection{Kind: "redis", Host: "db.example"}})
		result <- err
	}()
	<-started
	if err := service.CloseAccountSession("login-a"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; err == nil {
		t.Fatal("connection registered after its login session was revoked")
	}
	<-closed
	if _, err := service.connOf(ctx, "late-db"); err == nil {
		t.Fatal("revoked login connection remained in the registry")
	}
}

func TestAssetConnectUsesResolvedCredentialAndOptions(t *testing.T) {
	host, username := "redis.internal", "acl-user"
	port := 6380
	resolver := AssetResolverFunc(func(_ context.Context, id string) (Asset, error) {
		if id != "asset-1" {
			t.Fatalf("asset id=%q", id)
		}
		return Asset{
			Kind: "redis", Host: &host, Port: &port, Username: &username, Password: "decrypted",
			OptionsJSON: `{"database":"2","tls":{"mode":"required","insecureSkipVerify":true}}`,
		}, nil
	})
	service := NewService(resolver)
	service.newID = func() (string, error) { return "redis-id", nil }
	var captured RedisConfig
	service.openRedis = func(_ context.Context, config RedisConfig) (*redisConnection, error) {
		captured = config
		return &redisConnection{client: &fakeRedis{}, console: &fakeRedis{}}, nil
	}
	if _, err := service.Connect(context.Background(), ConnectArgs{AssetID: "asset-1"}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if captured.Host != host || captured.Port != 6380 || captured.Username != username || captured.Password != "decrypted" || captured.Database != 2 || captured.TLSConfig == nil {
		t.Fatalf("captured config: %+v", captured)
	}
}

func TestConnectValidationAndErrorCodes(t *testing.T) {
	service := NewService(nil)
	for _, args := range []ConnectArgs{
		{},
		{Inline: &InlineConnection{Kind: "mongodb"}},
		{Inline: &InlineConnection{Kind: "redis", Database: "-1"}},
		{AssetID: "missing"},
	} {
		_, err := service.Connect(context.Background(), args)
		var ipcErr *ipc.Error
		if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeBadParam {
			t.Errorf("args=%+v error=%#v", args, err)
		}
	}
}

func TestDisconnectFailureIsSurfacedAndHandleReleased(t *testing.T) {
	service := NewService(nil)
	service.conns["bad"] = &redisConnection{closeFn: func() error { return errors.New("driver: bad conn") }}

	err := service.Disconnect(context.Background(), "bad")
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeInternal || !strings.Contains(ipcErr.Message, "关闭数据库连接") {
		t.Fatalf("disconnect error=%#v", err)
	}
	if _, err := service.redisOf("bad"); err == nil {
		t.Fatal("connection handle survived failed disconnect")
	}
	if err := service.Disconnect(context.Background(), "bad"); err != nil {
		t.Fatalf("retry after failed disconnect: %v", err)
	}
}

func TestWrongKindAndCloseAll(t *testing.T) {
	service := NewService(nil)
	closed := 0
	service.conns["redis"] = &redisConnection{client: &fakeRedis{}, console: &fakeRedis{}, closeFn: func() error { closed++; return nil }}
	_, err := service.Schemas(context.Background(), "redis")
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeBadParam {
		t.Fatalf("wrong-kind error: %#v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := service.Close(); err != nil || closed != 1 {
		t.Fatalf("second close: err=%v closed=%d", err, closed)
	}
	if !reflect.DeepEqual(service.conns, map[string]managedConnection{}) {
		t.Fatalf("connections remain after close: %+v", service.conns)
	}
}

func TestServiceErrorsHaveEnglishModelProjection(t *testing.T) {
	ctx := context.Background()
	const driverDetail = `driver: schema "schema-用户", connId "conn-用户", table "table-用户", literal "没有资产"`
	describeError := func(kind, queryPart string) func() error {
		return func() error {
			service := NewService(nil)
			state := &fakeSQLState{query: func(_ context.Context, statement string) (driver.Rows, error) {
				if strings.Contains(statement, queryPart) {
					return nil, errors.New(driverDetail)
				}
				return newFakeRows([]string{"value"}, nil, nil), nil
			}}
			if kind == "mysql" {
				service.conns["conn-用户"] = &mysqlConnection{db: newFakeSQLDB(t, state)}
			} else {
				service.conns["conn-用户"] = &postgresConnection{db: newFakeSQLDB(t, state)}
			}
			_, err := service.Describe(ctx, "conn-用户", "schema-用户", "table-用户")
			return err
		}
	}
	missingCA := filepath.Join(t.TempDir(), "missing-用户.pem")
	_, caReadErr := os.ReadFile(missingCA)
	invalidCA := filepath.Join(t.TempDir(), "invalid-用户.pem")
	if err := os.WriteFile(invalidCA, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		tool    string
		trigger func() error
		source  string
		want    string
	}{
		{
			name: "unknown connection",
			tool: "db_list_tables",
			trigger: func() error {
				_, err := NewService(nil).Tables(ctx, "conn-用户-1", "schema-用户")
				return err
			},
			source: "数据库连接 conn-用户-1 不存在或已关闭",
			want:   "database connection conn-用户-1 does not exist or is closed",
		},
		{
			name: "missing schema",
			tool: "db_list_tables",
			trigger: func() error {
				service := NewService(nil)
				service.conns["pg-empty"] = &postgresConnection{db: newFakeSQLDB(t, &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) {
					return newFakeRows([]string{"current_schema"}, nil, [][]driver.Value{{nil}}), nil
				}})}
				_, err := service.Tables(ctx, "pg-empty", "")
				return err
			},
			source: "参数错误: 请选择 schema",
			want:   "invalid parameters: select a schema",
		},
		{
			name: "query timeout",
			tool: "db_query",
			trigger: func() error {
				service := NewService(nil)
				service.conns["pg-timeout"] = &postgresConnection{db: newFakeSQLDB(t, &fakeSQLState{query: func(ctx context.Context, _ string) (driver.Rows, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				}})}
				_, err := service.Query(ctx, "pg-timeout", "SELECT pg_sleep(10)", 10, 5*time.Millisecond)
				return err
			},
			source: "SQL 执行超时（5ms）",
			want:   "SQL execution timed out (5ms)",
		},
		{
			name: "internal schema read failure",
			tool: "db_list_tables",
			trigger: func() error {
				service := NewService(nil)
				service.conns["pg-read"] = &postgresConnection{db: newFakeSQLDB(t, &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) {
					return nil, errors.New(`schema "schema-用户" does not exist`)
				}})}
				_, err := service.Tables(ctx, "pg-read", "schema-用户")
				return err
			},
			source: `内部错误: 读取 PostgreSQL 表列表: schema "schema-用户" does not exist`,
			want:   `internal error: failed to read PostgreSQL table list: schema "schema-用户" does not exist`,
		},
		{
			name: "redis does not support relational tables",
			tool: "db_list_tables",
			trigger: func() error {
				_, err := serviceWithFakeRedis(&fakeRedis{}).Tables(ctx, "redis", "schema-用户")
				return err
			},
			source: "参数错误: Redis 连接不支持此操作",
			want:   "invalid parameters: this operation is not supported for Redis connections",
		},
		{
			name: "redis scan rejects another connection type",
			tool: "redis_scan",
			trigger: func() error {
				service := NewService(nil)
				service.conns["mysql-用户"] = &mysqlConnection{}
				_, err := service.RedisScan(ctx, "mysql-用户", 0, "schema-用户:*", 100)
				return err
			},
			source: "参数错误: 该连接不是 Redis",
			want:   "invalid parameters: the connection is not Redis",
		},
		{
			name: "redis TYPE failure",
			tool: "db_query",
			trigger: func() error {
				_, err := serviceWithFakeRedis(&fakeRedis{typeErr: errors.New("没有资产")}).RedisInspect(ctx, "redis", "key-用户")
				return err
			},
			source: "内部错误: Redis TYPE 失败: 没有资产",
			want:   "internal error: Redis TYPE failed: 没有资产",
		},
		{
			name:    "mysql columns",
			tool:    "db_describe",
			trigger: describeError("mysql", "information_schema.columns"),
			source:  "内部错误: 读取 MySQL 字段列表: " + driverDetail,
			want:    "internal error: failed to read MySQL column list: " + driverDetail,
		},
		{
			name: "mysql driver detail is opaque",
			tool: "db_describe",
			trigger: func() error {
				service := NewService(nil)
				service.conns["mysql-opaque"] = &mysqlConnection{db: newFakeSQLDB(t, &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) {
					return nil, errors.New("没有资产")
				}})}
				_, err := service.Describe(ctx, "mysql-opaque", "schema-用户", "table-用户")
				return err
			},
			source: "内部错误: 读取 MySQL 字段列表: 没有资产",
			want:   "internal error: failed to read MySQL column list: 没有资产",
		},
		{
			name:    "mysql indexes",
			tool:    "db_describe",
			trigger: describeError("mysql", "information_schema.statistics"),
			source:  "内部错误: 读取 MySQL 索引列表: " + driverDetail,
			want:    "internal error: failed to read MySQL index list: " + driverDetail,
		},
		{
			name: "mysql cancellation",
			tool: "db_query",
			trigger: func() error {
				canceledCtx, cancel := context.WithCancel(ctx)
				cancel()
				service := NewService(nil)
				service.conns["mysql-cancel"] = &mysqlConnection{db: newFakeSQLDB(t, &fakeSQLState{})}
				_, err := service.Query(canceledCtx, "mysql-cancel", "SELECT 1", 10, time.Second)
				return err
			},
			source: "内部错误: SQL 执行已取消: context canceled",
			want:   "internal error: SQL execution was canceled: context canceled",
		},
		{
			name:    "postgres primary key",
			tool:    "db_describe",
			trigger: describeError("postgres", "pg_constraint"),
			source:  "内部错误: 读取 PostgreSQL 主键: " + driverDetail,
			want:    "internal error: failed to read PostgreSQL primary key: " + driverDetail,
		},
		{
			name:    "postgres columns",
			tool:    "db_describe",
			trigger: describeError("postgres", "information_schema.columns"),
			source:  "内部错误: 读取 PostgreSQL 字段列表: " + driverDetail,
			want:    "internal error: failed to read PostgreSQL column list: " + driverDetail,
		},
		{
			name:    "postgres indexes",
			tool:    "db_describe",
			trigger: describeError("postgres", "pg_index"),
			source:  "内部错误: 读取 PostgreSQL 索引列表: " + driverDetail,
			want:    "internal error: failed to read PostgreSQL index list: " + driverDetail,
		},
		{
			name: "redis scan",
			tool: "redis_scan",
			trigger: func() error {
				const detail = `ERR unknown command 'SCAN', with args beginning with: 'schema-用户'`
				_, err := serviceWithFakeRedis(&fakeRedis{scanErr: errors.New(detail)}).RedisScan(ctx, "redis", 0, "schema-用户:*", 100)
				return err
			},
			source: "内部错误: Redis SCAN 失败: ERR unknown command 'SCAN', with args beginning with: 'schema-用户'",
			want:   "internal error: Redis SCAN failed: ERR unknown command 'SCAN', with args beginning with: 'schema-用户'",
		},
		{
			name: "unsupported TLS mode",
			tool: "db_list_tables",
			trigger: func() error {
				_, err := NewService(nil).Connect(ctx, ConnectArgs{Inline: &InlineConnection{Kind: "mysql", Host: "db-用户", TLS: &TLSOptions{Mode: "verify-用户"}}})
				return err
			},
			source: "参数错误: 不支持的 TLS 模式 verify-用户",
			want:   "invalid parameters: unsupported TLS mode verify-用户",
		},
		{
			name: "TLS CA read error",
			tool: "db_list_tables",
			trigger: func() error {
				_, err := NewService(nil).Connect(ctx, ConnectArgs{Inline: &InlineConnection{Kind: "mysql", Host: "db-用户", TLS: &TLSOptions{CAFile: missingCA}}})
				return err
			},
			source: "参数错误: 读取 TLS CA: " + caReadErr.Error(),
			want:   "invalid parameters: failed to read TLS CA: " + caReadErr.Error(),
		},
		{
			name: "invalid TLS CA",
			tool: "db_list_tables",
			trigger: func() error {
				_, err := NewService(nil).Connect(ctx, ConnectArgs{Inline: &InlineConnection{Kind: "postgres", Host: "db-用户", TLS: &TLSOptions{CAFile: invalidCA}}})
				return err
			},
			source: "参数错误: TLS CA 文件没有有效证书",
			want:   "invalid parameters: TLS CA file contains no valid certificates",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.trigger()
			if err == nil || err.Error() != test.source {
				t.Fatalf("service error = %v, want %q", err, test.source)
			}
			content, err := json.Marshal(map[string]any{"ok": false, "text": err.Error(), "exitCode": 1, "truncated": false})
			if err != nil {
				t.Fatal(err)
			}
			message := schema.ToolMessage(string(content), "call-1")
			message.ToolName = test.tool
			projected := provider.ProjectModelMessages([]*schema.Message{message})
			var envelope struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(projected[0].Content), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Text != test.want {
				t.Fatalf("projected error = %q, want %q", envelope.Text, test.want)
			}
		})
	}
}

func assertDBErrorProjection(t *testing.T, tool string, err error, source, want string, code ipc.Code) {
	t.Helper()
	if err == nil || err.Error() != source {
		t.Fatalf("service error = %v, want %q", err, source)
	}
	if code != "" {
		var ipcErr *ipc.Error
		if !errors.As(err, &ipcErr) || ipcErr.Code != code {
			t.Fatalf("error code = %#v, want %q", err, code)
		}
	}
	content, err := json.Marshal(map[string]any{"ok": false, "text": err.Error(), "exitCode": 1, "truncated": false})
	if err != nil {
		t.Fatal(err)
	}
	message := schema.ToolMessage(string(content), "call-projection")
	message.ToolName = tool
	projected := provider.ProjectModelMessages([]*schema.Message{message})
	var envelope struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(projected[0].Content), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Text != want {
		t.Fatalf("projected error = %q, want %q", envelope.Text, want)
	}
}

func TestServiceConnectionResolverAndIPCErrorProjection(t *testing.T) {
	ctx := context.Background()
	const driverDetail = `driver: host "db-用户", literal "没有资产"`
	openStore := func() *store.Store {
		database, err := store.OpenInMemory(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		return database
	}
	credentialAsset := func(decryptor CredentialDecryptor) error {
		database := openStore()
		credID := "credential-用户"
		if _, err := database.CredentialPut(ctx, store.CredentialInput{ID: credID, Name: "database", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}}); err != nil {
			t.Fatal(err)
		}
		row, err := database.AssetCreate(ctx, store.AssetInput{Kind: "mysql", Name: "database", CredID: &credID})
		if err != nil {
			t.Fatal(err)
		}
		_, err = NewStoreAssetResolver(database, decryptor).ResolveDBAsset(ctx, row.ID)
		return err
	}
	connectCanceled := func(kind string) func() error {
		return func() error {
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			_, err := NewService(nil).Connect(canceled, ConnectArgs{Inline: &InlineConnection{Kind: kind, Host: "127.0.0.1"}})
			return err
		}
	}
	tests := []struct {
		name    string
		tool    string
		trigger func() error
		source  string
		want    string
		code    ipc.Code
	}{
		{
			name: "mysql configuration",
			tool: "db_list_tables",
			trigger: func() error {
				service := NewService(nil)
				service.openMySQL = func(context.Context, MySQLConfig) (*mysqlConnection, error) {
					return nil, badParam(fmt.Errorf("MySQL 配置无效: %w", errors.New(driverDetail)))
				}
				_, err := service.Connect(ctx, ConnectArgs{Inline: &InlineConnection{Kind: "mysql", Host: "db-用户"}})
				return err
			},
			source: "参数错误: MySQL 配置无效: " + driverDetail,
			want:   "invalid parameters: invalid MySQL configuration: " + driverDetail,
			code:   ipc.CodeBadParam,
		},
		{
			name: "postgres configuration",
			tool: "db_list_tables",
			trigger: func() error {
				service := NewService(nil)
				service.openPostgres = func(context.Context, PostgresConfig) (*postgresConnection, error) {
					return nil, badParam(fmt.Errorf("PostgreSQL 配置无效: %w", errors.New(driverDetail)))
				}
				_, err := service.Connect(ctx, ConnectArgs{Inline: &InlineConnection{Kind: "postgres", Host: "db-用户"}})
				return err
			},
			source: "参数错误: PostgreSQL 配置无效: " + driverDetail,
			want:   "invalid parameters: invalid PostgreSQL configuration: " + driverDetail,
			code:   ipc.CodeBadParam,
		},
		{name: "mysql connection", tool: "db_list_tables", trigger: connectCanceled("mysql"), source: "内部错误: MySQL 连接失败: context canceled", want: "internal error: MySQL connection failed: context canceled", code: ipc.CodeInternal},
		{name: "postgres connection", tool: "db_list_tables", trigger: connectCanceled("postgres"), source: "内部错误: PostgreSQL 连接失败: context canceled", want: "internal error: PostgreSQL connection failed: context canceled", code: ipc.CodeInternal},
		{name: "redis connection", tool: "redis_scan", trigger: connectCanceled("redis"), source: "内部错误: Redis 连接失败: context canceled", want: "internal error: Redis connection failed: context canceled", code: ipc.CodeInternal},
		{
			name: "redis database number",
			tool: "redis_scan",
			trigger: func() error {
				_, err := NewService(nil).Connect(ctx, ConnectArgs{Inline: &InlineConnection{Kind: "redis", Database: "not-a-number"}})
				return err
			},
			source: "参数错误: Redis 数据库编号必须是非负整数",
			want:   "invalid parameters: Redis database number must be a non-negative integer",
			code:   ipc.CodeBadParam,
		},
		{
			name: "unsupported database type",
			tool: "db_list_tables",
			trigger: func() error {
				_, err := NewService(nil).Connect(ctx, ConnectArgs{Inline: &InlineConnection{Kind: "mongodb"}})
				return err
			},
			source: "参数错误: 不支持的数据库类型 mongodb",
			want:   "invalid parameters: unsupported database type mongodb",
			code:   ipc.CodeBadParam,
		},
		{
			name: "connection id generation",
			tool: "db_list_tables",
			trigger: func() error {
				service := NewService(nil)
				service.openMySQL = func(context.Context, MySQLConfig) (*mysqlConnection, error) {
					return &mysqlConnection{db: newFakeSQLDB(t, &fakeSQLState{})}, nil
				}
				service.newID = func() (string, error) { return "", errors.New("rng failure") }
				_, err := service.Connect(ctx, ConnectArgs{Inline: &InlineConnection{Kind: "mysql", Host: "db-用户"}})
				return err
			},
			source: "内部错误: 生成数据库连接 ID: rng failure",
			want:   "internal error: failed to generate database connection ID: rng failure",
			code:   ipc.CodeInternal,
		},
		{
			name: "closed service",
			tool: "redis_scan",
			trigger: func() error {
				service := NewService(nil)
				service.openRedis = func(context.Context, RedisConfig) (*redisConnection, error) {
					return &redisConnection{closeFn: func() error { return nil }}, nil
				}
				if err := service.Close(); err != nil {
					t.Fatal(err)
				}
				_, err := service.Connect(ctx, ConnectArgs{Inline: &InlineConnection{Kind: "redis", Host: "db-用户"}})
				return err
			},
			source: "连接已断开：数据库服务已关闭，请重新连接",
			want:   "disconnected: database service is closed; reconnect",
			code:   ipc.CodeDisconnected,
		},
		{
			name: "disconnect failure",
			tool: "db_list_tables",
			trigger: func() error {
				service := NewService(nil)
				service.conns["bad"] = &redisConnection{closeFn: func() error { return errors.New("close detail") }}
				return service.Disconnect(context.Background(), "bad")
			},
			source: "内部错误: 关闭数据库连接: close detail",
			want:   "internal error: failed to close database connection: close detail",
			code:   ipc.CodeInternal,
		},
		{
			name: "missing inline configuration",
			tool: "db_list_tables",
			trigger: func() error {
				_, err := NewService(nil).Connect(ctx, ConnectArgs{})
				return err
			},
			source: "参数错误: 需要提供数据库资产或连接配置",
			want:   "invalid parameters: database asset or connection configuration is required",
			code:   ipc.CodeBadParam,
		},
		{
			name: "missing asset resolver",
			tool: "db_list_tables",
			trigger: func() error {
				_, err := NewService(nil).Connect(ctx, ConnectArgs{AssetID: "asset-用户"})
				return err
			},
			source: "参数错误: 数据库资产解析器未配置",
			want:   "invalid parameters: database asset resolver is not configured",
			code:   ipc.CodeBadParam,
		},
		{
			name: "invalid asset options",
			tool: "db_list_tables",
			trigger: func() error {
				resolver := AssetResolverFunc(func(context.Context, string) (Asset, error) {
					return Asset{Kind: "mysql", OptionsJSON: "{"}, nil
				})
				_, err := NewService(resolver).Connect(ctx, ConnectArgs{AssetID: "asset-用户"})
				return err
			},
			source: "参数错误: 数据库资产配置无效: unexpected end of JSON input",
			want:   "invalid parameters: invalid database asset configuration: unexpected end of JSON input",
			code:   ipc.CodeBadParam,
		},
		{
			name: "asset port range",
			tool: "db_list_tables",
			trigger: func() error {
				port := 65536
				resolver := AssetResolverFunc(func(context.Context, string) (Asset, error) {
					return Asset{Kind: "mysql", Port: &port}, nil
				})
				_, err := NewService(resolver).Connect(ctx, ConnectArgs{AssetID: "asset-用户"})
				return err
			},
			source: "参数错误: 数据库端口超出范围",
			want:   "invalid parameters: database port is out of range",
			code:   ipc.CodeBadParam,
		},
		{
			name: "resolver missing asset store",
			tool: "db_list_tables",
			trigger: func() error {
				_, err := NewStoreAssetResolver(nil, nil).ResolveDBAsset(ctx, "asset-用户")
				return err
			},
			source: "内部错误: 读取数据库资产: 资产存储未配置",
			want:   "internal error: failed to read database asset: asset store is not configured",
			code:   ipc.CodeInternal,
		},
		{
			name:    "resolver missing credential decryptor",
			tool:    "db_list_tables",
			trigger: func() error { return credentialAsset(nil) },
			source:  "内部错误: 读取数据库凭据: 凭据解密器未配置",
			want:    "internal error: failed to read database credential: credential decryptor is not configured",
			code:    ipc.CodeInternal,
		},
		{
			name: "resolver missing asset",
			tool: "db_list_tables",
			trigger: func() error {
				_, err := NewStoreAssetResolver(openStore(), nil).ResolveDBAsset(ctx, "missing-用户")
				return err
			},
			source: "资产不存在",
			want:   "asset not found",
			code:   ipc.CodeNotFound,
		},
		{
			name: "ipc query timeout range",
			tool: "db_query",
			trigger: func() error {
				service := NewService(nil)
				dispatcher := ipc.NewDispatcher()
				if err := service.RegisterCommands(dispatcher); err != nil {
					t.Fatal(err)
				}
				args, err := json.Marshal(map[string]any{"connId": "conn-用户", "sql": "SELECT 1", "timeoutMs": uint64(math.MaxInt64/int64(time.Millisecond)) + 1})
				if err != nil {
					t.Fatal(err)
				}
				response := dispatcher.Dispatch(ctx, ipc.Request{Command: "db_query", Args: args}, ipc.Environment{})
				if response.Error == nil {
					return errors.New("db_query unexpectedly succeeded")
				}
				return response.Error
			},
			source: "参数错误: 查询超时时间（毫秒）超出范围",
			want:   "invalid parameters: query timeout in milliseconds is out of range",
			code:   ipc.CodeBadParam,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertDBErrorProjection(t, test.tool, test.trigger(), test.source, test.want, test.code)
		})
	}
}

func TestServiceMetadataAndRedisErrorProjection(t *testing.T) {
	ctx := context.Background()
	const driverDetail = `driver: schema "schema-用户", key "key-用户", literal "没有资产"`
	metadataService := func(kind string) *Service {
		service := NewService(nil)
		state := &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) {
			return nil, errors.New(driverDetail)
		}}
		if kind == "mysql" {
			service.conns["conn"] = &mysqlConnection{db: newFakeSQLDB(t, state)}
		} else {
			service.conns["conn"] = &postgresConnection{db: newFakeSQLDB(t, state)}
		}
		return service
	}
	emptySchemaService := func(kind string) *Service {
		service := NewService(nil)
		state := &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) {
			return newFakeRows([]string{"current"}, nil, [][]driver.Value{{nil}}), nil
		}}
		if kind == "mysql" {
			service.conns["conn"] = &mysqlConnection{db: newFakeSQLDB(t, state)}
		} else {
			service.conns["conn"] = &postgresConnection{db: newFakeSQLDB(t, state)}
		}
		return service
	}
	tests := []struct {
		name    string
		tool    string
		trigger func() error
		source  string
		want    string
		code    ipc.Code
	}{
		{name: "mysql schema list", tool: "db_list_tables", trigger: func() error { _, err := metadataService("mysql").Schemas(ctx, "conn"); return err }, source: "内部错误: 读取 MySQL 数据库列表: " + driverDetail, want: "internal error: failed to read MySQL database list: " + driverDetail, code: ipc.CodeInternal},
		{name: "mysql table list", tool: "db_list_tables", trigger: func() error { _, err := metadataService("mysql").Tables(ctx, "conn", "schema-用户"); return err }, source: "内部错误: 读取 MySQL 表列表: " + driverDetail, want: "internal error: failed to read MySQL table list: " + driverDetail, code: ipc.CodeInternal},
		{name: "mysql current database", tool: "db_list_tables", trigger: func() error { _, err := metadataService("mysql").Tables(ctx, "conn", ""); return err }, source: "内部错误: 读取当前 MySQL 数据库: " + driverDetail, want: "internal error: failed to read current MySQL database: " + driverDetail, code: ipc.CodeInternal},
		{name: "mysql missing database", tool: "db_list_tables", trigger: func() error { _, err := emptySchemaService("mysql").Tables(ctx, "conn", ""); return err }, source: "参数错误: 未选择数据库，请先选择 schema", want: "invalid parameters: no database selected; select a schema first", code: ipc.CodeBadParam},
		{name: "postgres schema list", tool: "db_list_tables", trigger: func() error { _, err := metadataService("postgres").Schemas(ctx, "conn"); return err }, source: "内部错误: 读取 PostgreSQL schema 列表: " + driverDetail, want: "internal error: failed to read PostgreSQL schema list: " + driverDetail, code: ipc.CodeInternal},
		{name: "postgres table list", tool: "db_list_tables", trigger: func() error { _, err := metadataService("postgres").Tables(ctx, "conn", "schema-用户"); return err }, source: "内部错误: 读取 PostgreSQL 表列表: " + driverDetail, want: "internal error: failed to read PostgreSQL table list: " + driverDetail, code: ipc.CodeInternal},
		{name: "postgres current schema", tool: "db_list_tables", trigger: func() error { _, err := metadataService("postgres").Tables(ctx, "conn", ""); return err }, source: "内部错误: 读取当前 PostgreSQL schema: " + driverDetail, want: "internal error: failed to read current PostgreSQL schema: " + driverDetail, code: ipc.CodeInternal},
		{name: "mysql empty table", tool: "db_describe", trigger: func() error {
			service := NewService(nil)
			service.conns["conn"] = &mysqlConnection{}
			_, err := service.Describe(ctx, "conn", "schema-用户", "")
			return err
		}, source: "参数错误: 表名不能为空", want: "invalid parameters: table name cannot be empty", code: ipc.CodeBadParam},
		{name: "postgres empty table", tool: "db_describe", trigger: func() error {
			service := NewService(nil)
			service.conns["conn"] = &postgresConnection{}
			_, err := service.Describe(ctx, "conn", "schema-用户", "")
			return err
		}, source: "参数错误: 表名不能为空", want: "invalid parameters: table name cannot be empty", code: ipc.CodeBadParam},
		{name: "redis schemas unsupported", tool: "db_list_tables", trigger: func() error { _, err := serviceWithFakeRedis(&fakeRedis{}).Schemas(ctx, "redis"); return err }, source: "参数错误: Redis 连接不支持此操作", want: "invalid parameters: this operation is not supported for Redis connections", code: ipc.CodeBadParam},
		{name: "redis describe unsupported", tool: "db_describe", trigger: func() error {
			_, err := serviceWithFakeRedis(&fakeRedis{}).Describe(ctx, "redis", "schema-用户", "table-用户")
			return err
		}, source: "参数错误: Redis 连接不支持此操作", want: "invalid parameters: this operation is not supported for Redis connections", code: ipc.CodeBadParam},
		{name: "redis query unsupported", tool: "db_query", trigger: func() error {
			_, err := serviceWithFakeRedis(&fakeRedis{}).Query(ctx, "redis", "SELECT 1", 1, time.Second)
			return err
		}, source: "参数错误: Redis 连接不支持此操作", want: "invalid parameters: this operation is not supported for Redis connections", code: ipc.CodeBadParam},
		{name: "mysql wrong connection type", tool: "db_list_tables", trigger: func() error {
			service := NewService(nil)
			service.conns["conn"] = &postgresConnection{}
			_, err := service.mysqlOf(context.Background(), "conn")
			return err
		}, source: "参数错误: 该连接不是 MySQL", want: "invalid parameters: the connection is not MySQL", code: ipc.CodeBadParam},
		{name: "postgres wrong connection type", tool: "db_list_tables", trigger: func() error {
			service := NewService(nil)
			service.conns["conn"] = &mysqlConnection{}
			_, err := service.postgresOf(context.Background(), "conn")
			return err
		}, source: "参数错误: 该连接不是 PostgreSQL", want: "invalid parameters: the connection is not PostgreSQL", code: ipc.CodeBadParam},
		{name: "redis wrong connection type", tool: "redis_scan", trigger: func() error {
			service := NewService(nil)
			service.conns["conn"] = &mysqlConnection{}
			_, err := service.redisOf("conn")
			return err
		}, source: "参数错误: 该连接不是 Redis", want: "invalid parameters: the connection is not Redis", code: ipc.CodeBadParam},
		{name: "redis ttl", tool: "db_query", trigger: func() error {
			_, err := serviceWithFakeRedis(&fakeRedis{keyType: "none", ttlErr: errors.New(driverDetail)}).RedisInspect(ctx, "redis", "key-用户")
			return err
		}, source: "内部错误: Redis TTL 失败: " + driverDetail, want: "internal error: Redis TTL failed: " + driverDetail, code: ipc.CodeInternal},
		{name: "redis key value", tool: "db_query", trigger: func() error {
			_, err := serviceWithFakeRedis(&fakeRedis{keyType: "string", stringErr: errors.New(driverDetail)}).RedisInspect(ctx, "redis", "key-用户")
			return err
		}, source: "内部错误: Redis 读取键值失败: " + driverDetail, want: "internal error: failed to read Redis key value: " + driverDetail, code: ipc.CodeInternal},
		{name: "redis set ttl", tool: "db_query", trigger: func() error {
			return serviceWithFakeRedis(&fakeRedis{doFunc: func(...any) (any, error) { return nil, errors.New(driverDetail) }}).RedisSetTTL(ctx, "redis", "key-用户", 10)
		}, source: "内部错误: Redis 设置 TTL 失败: " + driverDetail, want: "internal error: failed to set Redis TTL: " + driverDetail, code: ipc.CodeInternal},
		{name: "redis empty command", tool: "db_query", trigger: func() error { _, err := serviceWithFakeRedis(&fakeRedis{}).RedisCommand(ctx, "redis", nil); return err }, source: "参数错误: Redis 命令为空", want: "invalid parameters: Redis command is empty", code: ipc.CodeBadParam},
		{name: "redis command failure", tool: "db_query", trigger: func() error {
			_, err := serviceWithFakeRedis(&fakeRedis{doFunc: func(...any) (any, error) { return nil, errors.New(driverDetail) }}).RedisCommand(ctx, "redis", []string{"GET", "key-用户"})
			return err
		}, source: "内部错误: Redis 命令失败: " + driverDetail, want: "internal error: Redis command failed: " + driverDetail, code: ipc.CodeInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertDBErrorProjection(t, test.tool, test.trigger(), test.source, test.want, test.code)
		})
	}
}
