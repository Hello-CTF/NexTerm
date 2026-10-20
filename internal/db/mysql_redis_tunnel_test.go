package db

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestAssetMySQLAndRedisTunnelPreserveTargetAndTLS(t *testing.T) {
	for _, test := range []struct {
		kind     string
		database string
	}{
		{kind: "mysql", database: "shop"},
		{kind: "redis", database: "2"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			host, username := test.kind+".tunnel.invalid", "app"
			port := 6543
			resolver := AssetResolverFunc(func(context.Context, string) (Asset, error) {
				return Asset{
					Kind: test.kind, Host: &host, Port: &port, Username: &username, Password: "secret",
					OptionsJSON: fmt.Sprintf(`{"database":%q,"viaAssetId":"  ssh-asset  ","tls":{"mode":"required","serverName":"db.sni.test"}}`, test.database),
				}, nil
			})
			factoryAsset := ""
			dial := DialContextFunc(func(_ context.Context, network, address string) (net.Conn, error) {
				if network != "tcp" || address != host+":6543" {
					t.Fatalf("dial target = %s %s", network, address)
				}
				return nil, errors.New("stop after target check")
			})
			service := NewService(resolver, WithDialerFactory(func(_ context.Context, assetID string) (DialContextFunc, error) {
				factoryAsset = assetID
				return dial, nil
			}))
			service.newID = func() (string, error) { return test.kind + "-tunnel", nil }
			var capturedDial DialContextFunc
			switch test.kind {
			case "mysql":
				service.openMySQL = func(_ context.Context, config MySQLConfig) (*mysqlConnection, error) {
					if config.Host != host || config.Port != uint16(port) || config.Username != username || config.Password != "secret" || config.Database != test.database || config.TLSConfig == nil || config.TLSConfig.ServerName != "db.sni.test" {
						t.Fatalf("captured config = %+v", config)
					}
					capturedDial = config.DialContext
					return &mysqlConnection{db: newFakeSQLDB(t, &fakeSQLState{})}, nil
				}
			case "redis":
				service.openRedis = func(_ context.Context, config RedisConfig) (*redisConnection, error) {
					if config.Host != host || config.Port != uint16(port) || config.Username != username || config.Password != "secret" || config.Database != 2 || config.TLSConfig == nil || config.TLSConfig.ServerName != "db.sni.test" {
						t.Fatalf("captured config = %+v", config)
					}
					capturedDial = config.DialContext
					return &redisConnection{client: &fakeRedis{}, console: &fakeRedis{}}, nil
				}
			}
			t.Cleanup(func() { _ = service.Close() })

			if _, err := service.Connect(t.Context(), ConnectArgs{AssetID: "db-asset"}); err != nil {
				t.Fatalf("connect: %v", err)
			}
			if factoryAsset != "ssh-asset" {
				t.Fatalf("factory asset = %q", factoryAsset)
			}
			if capturedDial == nil {
				t.Fatal("tunnel dialer was not installed")
			}
			if _, err := capturedDial(t.Context(), "tcp", host+":6543"); err == nil || err.Error() != "stop after target check" {
				t.Fatalf("installed dialer error = %v", err)
			}
		})
	}
}

func TestConnectMySQLAndRedisDialFunctionsPreserveTunnelErrors(t *testing.T) {
	for _, kind := range []string{"mysql", "redis"} {
		for _, test := range []struct {
			name string
			err  error
			code ipc.Code
		}{
			{name: "dial failure", err: errors.New("dial through current SSH failed")},
			{name: "disconnected", err: ipc.NewError(ipc.CodeDisconnected, "SSH 未连接"), code: ipc.CodeDisconnected},
			{name: "forbidden", err: ipc.NewError(ipc.CodeForbidden, "跨用户"), code: ipc.CodeForbidden},
		} {
			t.Run(kind+"/"+test.name, func(t *testing.T) {
				var called atomic.Int32
				host := kind + ".only.internal"
				dial := DialContextFunc(func(_ context.Context, network, address string) (net.Conn, error) {
					called.Add(1)
					if network != "tcp" || address != host+":6543" {
						return nil, fmt.Errorf("unexpected dial target %s %s", network, address)
					}
					return nil, test.err
				})
				var err error
				if kind == "mysql" {
					_, err = connectMySQL(t.Context(), MySQLConfig{Host: host, Port: 6543, Username: "app", Database: "shop", DialContext: dial})
				} else {
					_, err = connectRedis(t.Context(), RedisConfig{Host: host, Port: 6543, Username: "app", DialContext: dial})
				}
				if err == nil || !errors.Is(err, test.err) || called.Load() == 0 {
					t.Fatalf("err = %v, calls = %d", err, called.Load())
				}
				if test.code != "" {
					var ipcErr *ipc.Error
					if !errors.As(err, &ipcErr) || ipcErr.Code != test.code {
						t.Fatalf("error = %#v, want code %s", err, test.code)
					}
				}
			})
		}
	}
}
