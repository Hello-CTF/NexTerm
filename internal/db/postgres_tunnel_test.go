package db

import (
	"context"
	"database/sql/driver"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestAssetPostgresTunnelPreservesTargetAndTLS(t *testing.T) {
	host, username := "pg.tunnel.invalid", "app"
	port := 6543
	resolver := AssetResolverFunc(func(context.Context, string) (Asset, error) {
		return Asset{
			Kind: "postgres", Host: &host, Port: &port, Username: &username, Password: "secret",
			OptionsJSON: `{"database":"shop","viaAssetId":"  ssh-asset  ","tls":{"mode":"required","serverName":"pg.sni.test"}}`,
		}, nil
	})
	var factoryAsset string
	dial := DialContextFunc(func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "pg.tunnel.invalid:6543" {
			t.Fatalf("dial target = %s %s", network, address)
		}
		return nil, errors.New("stop after target check")
	})
	service := NewService(resolver, WithDialerFactory(func(_ context.Context, assetID string) (DialContextFunc, error) {
		factoryAsset = assetID
		return dial, nil
	}))
	service.newID = func() (string, error) { return "pg-tunnel", nil }
	var captured PostgresConfig
	service.openPostgres = func(_ context.Context, config PostgresConfig) (*postgresConnection, error) {
		captured = config
		return &postgresConnection{db: newFakeSQLDB(t, &fakeSQLState{})}, nil
	}
	t.Cleanup(func() { _ = service.Close() })

	if _, err := service.Connect(t.Context(), ConnectArgs{AssetID: "db-asset"}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if factoryAsset != "ssh-asset" {
		t.Fatalf("factory asset = %q", factoryAsset)
	}
	if captured.Host != host || captured.Port != uint16(port) || captured.Database != "shop" || captured.TLSConfig == nil || captured.TLSConfig.ServerName != "pg.sni.test" || captured.DialContext == nil {
		t.Fatalf("captured config = %+v", captured)
	}
	if _, err := captured.DialContext(t.Context(), "tcp", "pg.tunnel.invalid:6543"); err == nil || !strings.Contains(err.Error(), "stop after") {
		t.Fatalf("installed dialer error = %v", err)
	}
}

func TestConnectPostgresDialFuncUsesOriginalTargetAndPreservesTunnelError(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code ipc.Code
	}{
		{name: "dial failure", err: errors.New("dial through current SSH failed")},
		{name: "disconnected", err: ipc.NewError(ipc.CodeDisconnected, "SSH 未连接"), code: ipc.CodeDisconnected},
		{name: "forbidden", err: ipc.NewError(ipc.CodeForbidden, "跨用户"), code: ipc.CodeForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := 0
			_, err := connectPostgres(t.Context(), PostgresConfig{
				Host: "pg.only.internal", Port: 6543, Username: "app", Database: "shop",
				DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
					called++
					if network != "tcp" || address != "pg.only.internal:6543" {
						t.Fatalf("dial target = %s %s", network, address)
					}
					return nil, test.err
				},
			})
			if err == nil || !errors.Is(err, test.err) || called != 1 {
				t.Fatalf("err = %v, calls = %d", err, called)
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

func TestPostgresRedialPreservesTunnelErrorClassification(t *testing.T) {
	dialErr := ipc.NewError(ipc.CodeDisconnected, "SSH 跳板会话已断开")
	database := newFakeSQLDB(t, &fakeSQLState{query: func(context.Context, string) (driver.Rows, error) {
		return nil, dialErr
	}})
	defer database.Close()

	result, err := queryPostgres(t.Context(), database, "SELECT 1", 10, time.Second)
	if !errors.Is(err, dialErr) {
		t.Fatalf("query error = %v, want tunnel error", err)
	}
	if result.Error != nil {
		t.Fatalf("query result error = %q, want classified Go error", *result.Error)
	}

	_, err = postgresSchemas(t.Context(), &postgresConnection{db: database})
	if !errors.Is(err, dialErr) {
		t.Fatalf("metadata error = %v, want tunnel error", err)
	}
}

func TestViaAssetRequiresConfiguredFactory(t *testing.T) {
	for _, kind := range []string{"postgres", "mysql", "redis"} {
		t.Run(kind, func(t *testing.T) {
			host := "db.internal"
			resolver := AssetResolverFunc(func(context.Context, string) (Asset, error) {
				return Asset{Kind: kind, Host: &host, OptionsJSON: `{"viaAssetId":"ssh-asset"}`}, nil
			})
			service := NewService(resolver)
			_, err := service.Connect(t.Context(), ConnectArgs{AssetID: "db-asset"})
			var ipcErr *ipc.Error
			if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeUnsupported {
				t.Fatalf("error = %#v, want unsupported", err)
			}
		})
	}
}
