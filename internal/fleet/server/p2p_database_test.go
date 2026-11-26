package fleetserver

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

func TestP2PDeviceKeyDatabase(t *testing.T) {
	ctx := context.Background()
	database := dbtest.NewFixture(t).OpenStore(t)
	accounts := account.New(database.GORM())
	now := time.Now().UnixMilli()
	service, err := New(Config{DB: database.GORM(), Accounts: accounts}, WithNow(func() int64 { return now }))
	if err != nil {
		t.Fatal(err)
	}
	owner, err := accounts.CreateUser(ctx, "db-p2p-owner", "", "pw-123456")
	if err != nil {
		t.Fatal(err)
	}
	device, err := accounts.RegisterDevice(ctx, owner.ID, "db-p2p-box", "agent")
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := ed25519.GenerateKey(nil)
	second, _, _ := ed25519.GenerateKey(nil)
	register := func(key ed25519.PublicKey) agent.P2PKeyResponse {
		t.Helper()
		response, err := service.RegisterP2PDeviceKey(ctx, device.ID, grant.Key(key))
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	if response := register(first); response.State != agent.P2PKeyStateBound || response.PublicKey != grant.Key(first).String() {
		t.Fatalf("first = %+v", response)
	}
	if response := register(first); response.State != agent.P2PKeyStateBound {
		t.Fatalf("repeat = %+v", response)
	}
	if response := register(second); response.State != agent.P2PKeyStatePending || response.PublicKey != grant.Key(first).String() {
		t.Fatalf("changed = %+v", response)
	}
	row, err := service.deviceP2PKey(ctx, device.ID)
	if err != nil || row.PublicKey != grant.Key(first).String() || row.PendingKey != grant.Key(second).String() || row.PendingAt == nil || *row.PendingAt != now {
		t.Fatalf("pending row = %+v, %v", row, err)
	}
	identity := &account.Identity{UserID: owner.ID}
	if err := service.ConfirmP2PDeviceKey(ctx, identity, device.ID, grant.Key(first)); err == nil {
		t.Fatal("confirmed a key that is not pending")
	}
	if err := service.ConfirmP2PDeviceKey(ctx, identity, device.ID, grant.Key(second)); err != nil {
		t.Fatal(err)
	}
	row, err = service.deviceP2PKey(ctx, device.ID)
	if err != nil || row.PublicKey != grant.Key(second).String() || row.PendingKey != "" || row.PendingAt != nil || row.ConfirmedAt == nil {
		t.Fatalf("confirmed row = %+v, %v", row, err)
	}
	views, err := service.p2pKeyViews(ctx, []string{device.ID, "missing"})
	if err != nil || views[device.ID].State != agent.P2PKeyStateBound || views["missing"].State != agent.P2PKeyStateNone {
		t.Fatalf("views = %+v, %v", views, err)
	}

	// Deleting the device removes its key.
	userDevice := query.Use(database.GORM()).UserDevice
	if _, err := userDevice.WithContext(ctx).Where(userDevice.ID.Eq(device.ID)).Delete(); err != nil {
		t.Fatal(err)
	}
	if row, err := service.deviceP2PKey(ctx, device.ID); err != nil || row != nil {
		t.Fatalf("key after device delete = %+v, %v", row, err)
	}
}

func TestP2PSigningKeyDatabase(t *testing.T) {
	ctx := context.Background()
	database := dbtest.NewFixture(t).OpenStore(t)
	accounts := account.New(database.GORM())
	now := time.Now().UnixMilli()
	clock := func() int64 { return now }
	first, err := New(Config{DB: database.GORM(), Accounts: accounts}, WithNow(clock))
	if err != nil {
		t.Fatal(err)
	}
	signing, err := first.p2p.keys.signing(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	// A second instance on the same database signs with the stored key, so its grants verify with the first's keys.
	second, err := New(Config{DB: database.GORM(), Accounts: accounts}, WithNow(clock))
	if err != nil {
		t.Fatal(err)
	}
	again, err := second.p2p.keys.signing(ctx, second)
	if err != nil || again.KID != signing.KID || !again.Private.Equal(signing.Private) {
		t.Fatalf("second instance key = %v, %v", again.KID, err)
	}
	client, _, _ := ed25519.GenerateKey(nil)
	agentKey, _, _ := ed25519.GenerateKey(nil)
	token, err := grant.Issue(again, grant.Claims{
		GrantID: "g", DeviceID: "d", OwnerID: "u", ClientPub: grant.Key(client), AgentPub: grant.Key(agentKey),
		Scope: []string{"terminal"}, IssuedAt: now, ExpiresAt: now + grant.DefaultTTL.Milliseconds(),
	})
	if err != nil {
		t.Fatal(err)
	}
	published, err := first.p2p.keys.published(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grant.Verify(token, published, time.UnixMilli(now), "d", client, "terminal"); err != nil {
		t.Fatal(err)
	}

	now += p2pKeyRotation.Milliseconds()
	rotated, err := second.p2p.keys.signing(ctx, second)
	if err != nil || rotated.KID == signing.KID {
		t.Fatalf("rotated = %v, %v", rotated.KID, err)
	}
	now += p2pKeyRetention.Milliseconds()
	if _, err := second.p2p.keys.signing(ctx, second); err != nil {
		t.Fatal(err)
	}
	all, err := second.p2p.keys.all(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range all {
		if key.KID == signing.KID {
			t.Fatalf("expired key %s retained: %+v", signing.KID, all)
		}
	}
}
