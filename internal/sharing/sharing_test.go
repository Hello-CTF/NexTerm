package sharing

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

type serviceFixture struct {
	service  *Service
	accounts *account.Accounts
	db       *gorm.DB
	now      int64
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	accounts := account.New(database.GORM())
	fixture := &serviceFixture{accounts: accounts, db: database.GORM(), now: time.Now().UnixMilli()}
	service, err := New(Config{DB: database.GORM(), Accounts: accounts}, WithNow(func() int64 { return fixture.now }))
	if err != nil {
		t.Fatal(err)
	}
	fixture.service = service
	return fixture
}

func (f *serviceFixture) createUser(t *testing.T, username string) *account.User {
	t.Helper()
	user, err := f.accounts.CreateUser(context.Background(), username, "", "password-"+username)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func (f *serviceFixture) createSuperadmin(t *testing.T, username string) *account.User {
	t.Helper()
	code, err := f.accounts.GenerateInitCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	user, err := f.accounts.InitSuperadmin(context.Background(), code, username, "password-"+username)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func (f *serviceFixture) identity(user *account.User) *account.Identity {
	return &account.Identity{UserID: user.ID, Role: user.Role, State: user.State}
}

func (f *serviceFixture) createDevice(t *testing.T, owner *account.User, name string) string {
	t.Helper()
	deviceID := ids.New()
	row := store.UserDevice{ID: deviceID, UserID: owner.ID, Name: name, Kind: "desktop", CreatedAt: f.now}
	if err := query.Use(f.db).UserDevice.WithContext(context.Background()).Create(&row); err != nil {
		t.Fatal(err)
	}
	return deviceID
}

func (f *serviceFixture) createAgentDevice(t *testing.T, owner *account.User, name string) string {
	t.Helper()
	deviceID := f.createDevice(t, owner, name)
	userDevice := query.Use(f.db).UserDevice
	if _, err := userDevice.WithContext(context.Background()).Where(userDevice.ID.Eq(deviceID)).UpdateColumns(map[string]any{"kind": "agent"}); err != nil {
		t.Fatal(err)
	}
	credentialID := ids.New()
	credential := store.SyncCredential{ID: credentialID, UserID: owner.ID, DeviceID: deviceID, Purpose: "device-agent", SecretHash: "test-secret-" + credentialID, CreatedAt: f.now}
	if err := query.Use(f.db).SyncCredential.WithContext(context.Background()).Create(&credential); err != nil {
		t.Fatal(err)
	}
	lastSeen := f.now
	agent := store.DeviceAgent{DeviceID: deviceID, CredentialID: credentialID, Platform: "linux", CreatedAt: f.now, LastSeenAt: &lastSeen}
	if err := query.Use(f.db).DeviceAgent.WithContext(context.Background()).Create(&agent); err != nil {
		t.Fatal(err)
	}
	return deviceID
}

func (f *serviceFixture) insertHostShare(_ context.Context, identity *account.Identity, deviceID, recipientUsername string, write bool, ttl time.Duration) (*HostShare, error) {
	recipient, err := f.accounts.GetUserByUsername(context.Background(), recipientUsername)
	if err != nil {
		return nil, err
	}
	owner, err := f.accounts.GetUser(context.Background(), identity.UserID)
	if err != nil {
		return nil, err
	}
	ttl, err = normalizeTTL(ttl, defaultHostShareTTL)
	if err != nil {
		return nil, err
	}
	now := f.now
	share := &HostShare{
		ID: ids.New(), OwnerID: identity.UserID, OwnerUsername: owner.Username, DeviceID: deviceID,
		RecipientID: recipient.ID, RecipientUsername: recipient.Username, Permission: permissionFor(write),
		CreatedAt: now, ExpiresAt: now + ttl.Milliseconds(),
	}
	row := store.HostShare{
		ID: share.ID, OwnerID: share.OwnerID, DeviceID: share.DeviceID, RecipientID: share.RecipientID,
		Permission: string(share.Permission), CreatedAt: share.CreatedAt, ExpiresAt: share.ExpiresAt,
	}
	if err := query.Use(f.db).HostShare.WithContext(context.Background()).Create(&row); err != nil {
		return nil, err
	}
	return share, nil
}

func (f *serviceFixture) setAgentOnline(t *testing.T, deviceID string, online bool) {
	t.Helper()
	var lastSeen any
	if online {
		lastSeen = f.now
	}
	deviceAgent := query.Use(f.db).DeviceAgent
	if _, err := deviceAgent.WithContext(context.Background()).Where(deviceAgent.DeviceID.Eq(deviceID)).UpdateColumns(map[string]any{"last_seen_at": lastSeen}); err != nil {
		t.Fatal(err)
	}
}

func (f *serviceFixture) auditRows(t *testing.T, kind string) []map[string]any {
	t.Helper()
	audit := query.Use(f.db).AuditRow
	rows, err := audit.WithContext(context.Background()).
		Where(audit.Source.Eq(auditSourceSharing), audit.Kind.Eq(kind)).
		Order(audit.ID).
		Find()
	if err != nil {
		t.Fatal(err)
	}
	var payloads []map[string]any
	for _, row := range rows {
		var payload map[string]any
		if err := json.Unmarshal([]byte(row.PayloadJSON), &payload); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

func requireIPCCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %q, got nil", code)
	}
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("expected error code %q, got %v", code, err)
	}
}
