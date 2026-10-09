package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
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
	if err := service.Disconnect(result.ConnID); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if err := service.Disconnect(result.ConnID); err != nil {
		t.Fatalf("idempotent disconnect: %v", err)
	}
	_, err = service.mysqlOf(result.ConnID)
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeNotFound {
		t.Fatalf("connection survived disconnect: %#v", err)
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

	err := service.Disconnect("bad")
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeInternal || !strings.Contains(ipcErr.Message, "关闭数据库连接") {
		t.Fatalf("disconnect error=%#v", err)
	}
	if _, err := service.redisOf("bad"); err == nil {
		t.Fatal("connection handle survived failed disconnect")
	}
	if err := service.Disconnect("bad"); err != nil {
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
