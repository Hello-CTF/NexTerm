package db

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func identityTestService(t *testing.T, resolver AssetResolver, options ...ServiceOption) *Service {
	t.Helper()
	service := NewService(resolver, options...)
	database := newFakeSQLDB(t, &fakeSQLState{})
	service.openMySQL = func(_ context.Context, config MySQLConfig) (*mysqlConnection, error) {
		return &mysqlConnection{db: database, database: config.Database}, nil
	}
	service.openRedis = func(context.Context, RedisConfig) (*redisConnection, error) {
		return &redisConnection{closeFn: func() error { return nil }}, nil
	}
	return service
}

func mysqlAssetResolver(ids ...string) AssetResolver {
	known := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		known[id] = struct{}{}
	}
	return AssetResolverFunc(func(_ context.Context, id string) (Asset, error) {
		if _, ok := known[id]; !ok {
			return Asset{}, errors.New("资产不存在")
		}
		host := "db.internal"
		return Asset{Kind: "mysql", Host: &host}, nil
	})
}

func TestConnectRecordsAssetIdentityForAssetAndInline(t *testing.T) {
	service := identityTestService(t, mysqlAssetResolver("asset-1"))
	var sequence atomic.Uint64
	service.newID = func() (string, error) {
		return fmt.Sprintf("conn-%d", sequence.Add(1)), nil
	}
	ctx := ipc.WithUserID(t.Context(), "account-a")

	assetResult, err := service.Connect(ctx, ConnectArgs{AssetID: "asset-1"})
	if err != nil {
		t.Fatalf("asset connect: %v", err)
	}
	if assetID, ok := service.AssetIDOf(ctx, assetResult.ConnID); !ok || assetID != "asset-1" {
		t.Fatalf("AssetIDOf(asset) = %q, %v; want asset-1, true", assetID, ok)
	}

	inlineResult, err := service.Connect(ctx, ConnectArgs{Inline: &InlineConnection{Kind: "mysql", Host: "db.internal"}})
	if err != nil {
		t.Fatalf("inline connect: %v", err)
	}
	if assetID, ok := service.AssetIDOf(ctx, inlineResult.ConnID); ok {
		t.Fatalf("inline connection resolved asset identity %q; want explicit none", assetID)
	}
}

func TestAssetIDOfIsOwnerScoped(t *testing.T) {
	service := identityTestService(t, mysqlAssetResolver("asset-1"))
	service.newID = func() (string, error) { return "conn-1", nil }
	ownerA := ipc.WithUserID(t.Context(), "account-a")
	ownerB := ipc.WithUserID(t.Context(), "account-b")

	if _, err := service.Connect(ownerA, ConnectArgs{AssetID: "asset-1"}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if assetID, ok := service.AssetIDOf(ownerA, "conn-1"); !ok || assetID != "asset-1" {
		t.Fatalf("owner A AssetIDOf = %q, %v", assetID, ok)
	}
	if assetID, ok := service.AssetIDOf(ownerB, "conn-1"); ok {
		t.Fatalf("cross-owner lookup resolved %q; want explicit none", assetID)
	}
	if assetID, ok := service.AssetIDOf(context.Background(), "conn-1"); ok {
		t.Fatalf("identity-less lookup resolved %q; want explicit none", assetID)
	}
	if _, ok := service.AssetIDOf(ownerA, "conn-missing"); ok {
		t.Fatal("unknown connection resolved an asset identity")
	}
}

func TestAssetIDOfKeepsTunnelAssetSeparate(t *testing.T) {
	resolver := AssetResolverFunc(func(_ context.Context, id string) (Asset, error) {
		if id != "db-asset" {
			t.Fatalf("resolved asset id = %q", id)
		}
		host := "db.tunnel.invalid"
		return Asset{Kind: "mysql", Host: &host, OptionsJSON: `{"viaAssetId":"ssh-asset"}`}, nil
	})
	factoryAsset := ""
	var capturedDial DialContextFunc
	factory := WithDialerFactory(func(_ context.Context, viaAssetID string) (DialContextFunc, error) {
		factoryAsset = viaAssetID
		capturedDial = func(_ context.Context, network, address string) (net.Conn, error) {
			return nil, errors.New("stop after dial check")
		}
		return capturedDial, nil
	})
	service := identityTestService(t, resolver, factory)
	service.newID = func() (string, error) { return "conn-tunnel", nil }
	ctx := ipc.WithUserID(t.Context(), "account-a")

	if _, err := service.Connect(ctx, ConnectArgs{AssetID: "db-asset"}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if factoryAsset != "ssh-asset" {
		t.Fatalf("dialer factory asset = %q; want ssh-asset", factoryAsset)
	}
	if assetID, ok := service.AssetIDOf(ctx, "conn-tunnel"); !ok || assetID != "db-asset" {
		t.Fatalf("AssetIDOf = %q, %v; want db-asset, true (never the tunnel asset)", assetID, ok)
	}

	// A tunnel redial reuses the transport only; it must not disturb the DB asset identity.
	if _, err := capturedDial(t.Context(), "tcp", "db.tunnel.invalid:3306"); err == nil {
		t.Fatal("dial check error was swallowed")
	}
	if assetID, ok := service.AssetIDOf(ctx, "conn-tunnel"); !ok || assetID != "db-asset" {
		t.Fatalf("AssetIDOf after redial = %q, %v; want db-asset, true", assetID, ok)
	}
}

func TestAssetIDOfStaleAndReplacedConnections(t *testing.T) {
	service := identityTestService(t, mysqlAssetResolver("asset-1", "asset-2"))
	service.newID = func() (string, error) { return "conn-1", nil }
	ctx := ipc.WithUserID(t.Context(), "account-a")

	if _, err := service.Connect(ctx, ConnectArgs{AssetID: "asset-1"}); err != nil {
		t.Fatalf("connect asset-1: %v", err)
	}
	if err := service.Disconnect(ctx, "conn-1"); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if assetID, ok := service.AssetIDOf(ctx, "conn-1"); ok {
		t.Fatalf("disconnected connection resolved %q; want explicit none", assetID)
	}

	if _, err := service.Connect(ctx, ConnectArgs{Inline: &InlineConnection{Kind: "mysql", Host: "db.internal"}}); err != nil {
		t.Fatalf("inline reconnect: %v", err)
	}
	if assetID, ok := service.AssetIDOf(ctx, "conn-1"); ok {
		t.Fatalf("replaced inline connection inherited stale identity %q", assetID)
	}
	if err := service.Disconnect(ctx, "conn-1"); err != nil {
		t.Fatalf("disconnect inline: %v", err)
	}

	if _, err := service.Connect(ctx, ConnectArgs{AssetID: "asset-2"}); err != nil {
		t.Fatalf("connect asset-2: %v", err)
	}
	if assetID, ok := service.AssetIDOf(ctx, "conn-1"); !ok || assetID != "asset-2" {
		t.Fatalf("replaced connection AssetIDOf = %q, %v; want asset-2, true", assetID, ok)
	}
}

func TestAssetIDOfFollowsLoginSessionLifecycle(t *testing.T) {
	service := identityTestService(t, mysqlAssetResolver("asset-1"))
	service.newID = func() (string, error) { return "conn-1", nil }
	owner := ipc.WithUserID(t.Context(), "account-a")
	first := ipc.WithAccountSessionID(owner, "login-a")
	second := ipc.WithAccountSessionID(owner, "login-b")

	if _, err := service.Connect(first, ConnectArgs{AssetID: "asset-1"}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := service.connOf(second, "conn-1"); err != nil {
		t.Fatalf("adopt connection: %v", err)
	}
	if err := service.CloseAccountSession("login-a"); err != nil {
		t.Fatalf("revoke login-a: %v", err)
	}
	if assetID, ok := service.AssetIDOf(first, "conn-1"); ok {
		t.Fatalf("revoked login session resolved %q; want explicit none", assetID)
	}
	if assetID, ok := service.AssetIDOf(second, "conn-1"); !ok || assetID != "asset-1" {
		t.Fatalf("surviving login AssetIDOf = %q, %v; want asset-1, true", assetID, ok)
	}
	if err := service.CloseAccountSession("login-b"); err != nil {
		t.Fatalf("revoke login-b: %v", err)
	}
	if assetID, ok := service.AssetIDOf(second, "conn-1"); ok {
		t.Fatalf("identity survived last logout: %q", assetID)
	}
}

func TestAssetIDOfAbsentForFailedConnectsAndServiceClose(t *testing.T) {
	ctx := ipc.WithUserID(t.Context(), "account-a")

	failing := identityTestService(t, AssetResolverFunc(func(context.Context, string) (Asset, error) {
		return Asset{}, errors.New("资产不存在")
	}))
	failing.newID = func() (string, error) { return "conn-fail", nil }
	if _, err := failing.Connect(ctx, ConnectArgs{AssetID: "missing"}); err == nil {
		t.Fatal("connect with failing resolver succeeded")
	}
	if assetID, ok := failing.AssetIDOf(ctx, "conn-fail"); ok {
		t.Fatalf("failed connect left identity %q", assetID)
	}

	revoked := identityTestService(t, mysqlAssetResolver("asset-1"))
	revoked.newID = func() (string, error) { return "conn-revoked", nil }
	revokedCtx := ipc.WithAccountSessionCheck(ctx, func(context.Context) error {
		return ipc.NewError(ipc.CodeForbidden, "会话已吊销")
	})
	if _, err := revoked.Connect(revokedCtx, ConnectArgs{AssetID: "asset-1"}); err == nil {
		t.Fatal("connect with revoked account session succeeded")
	}
	if assetID, ok := revoked.AssetIDOf(ctx, "conn-revoked"); ok {
		t.Fatalf("revoked-session connect left identity %q", assetID)
	}

	closed := identityTestService(t, mysqlAssetResolver("asset-1"))
	closed.newID = func() (string, error) { return "conn-closed", nil }
	if _, err := closed.Connect(ctx, ConnectArgs{AssetID: "asset-1"}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if assetID, ok := closed.AssetIDOf(ctx, "conn-closed"); ok {
		t.Fatalf("closed service resolved %q; want explicit none", assetID)
	}
}

func TestAssetIDOfConcurrentLifecycle(t *testing.T) {
	service := identityTestService(t, mysqlAssetResolver("asset-1"))
	var sequence atomic.Uint64
	service.newID = func() (string, error) {
		return fmt.Sprintf("conn-%d", sequence.Add(1)), nil
	}
	ctx := ipc.WithUserID(t.Context(), "account-a")

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(assetBound bool) {
			defer wg.Done()
			args := ConnectArgs{Inline: &InlineConnection{Kind: "mysql", Host: "db.internal"}}
			if assetBound {
				args = ConnectArgs{AssetID: "asset-1"}
			}
			result, err := service.Connect(ctx, args)
			if err != nil {
				t.Errorf("connect: %v", err)
				return
			}
			assetID, ok := service.AssetIDOf(ctx, result.ConnID)
			if assetBound && (!ok || assetID != "asset-1") {
				t.Errorf("AssetIDOf(%s) = %q, %v; want asset-1, true", result.ConnID, assetID, ok)
			}
			if !assetBound && ok {
				t.Errorf("AssetIDOf(%s) = %q, %v; want explicit none", result.ConnID, assetID, ok)
			}
			if err := service.Disconnect(ctx, result.ConnID); err != nil {
				t.Errorf("disconnect %s: %v", result.ConnID, err)
			}
			if assetID, ok := service.AssetIDOf(ctx, result.ConnID); ok {
				t.Errorf("AssetIDOf(%s) after disconnect = %q, %v; want explicit none", result.ConnID, assetID, ok)
			}
		}(i%2 == 0)
	}
	wg.Wait()
}
