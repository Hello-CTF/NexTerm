package sharing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

type serviceFixture struct {
	service  *Service
	accounts *account.Accounts
	db       *sql.DB
	now      int64
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	accounts := account.New(database.DB())
	fixture := &serviceFixture{accounts: accounts, db: database.DB(), now: time.Now().UnixMilli()}
	service, err := New(Config{DB: database.DB(), Accounts: accounts}, WithNow(func() int64 { return fixture.now }))
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
	if _, err := f.db.ExecContext(context.Background(), `INSERT INTO user_device(id, user_id, name, kind, created_at)
VALUES(?,?,?,?,?)`, deviceID, owner.ID, name, "desktop", f.now); err != nil {
		t.Fatal(err)
	}
	return deviceID
}

func (f *serviceFixture) createAgentDevice(t *testing.T, owner *account.User, name string) string {
	t.Helper()
	deviceID := f.createDevice(t, owner, name)
	if _, err := f.db.ExecContext(context.Background(), `UPDATE user_device SET kind = 'agent' WHERE id = ?`, deviceID); err != nil {
		t.Fatal(err)
	}
	credentialID := ids.New()
	if _, err := f.db.ExecContext(context.Background(), `INSERT INTO sync_credential(id, user_id, device_id, purpose, secret_hash, created_at)
VALUES(?,?,?,?,?,?)`, credentialID, owner.ID, deviceID, "device-agent", "test-secret-"+credentialID, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(context.Background(), `INSERT INTO device_agent(device_id, credential_id, platform, created_at, last_seen_at)
VALUES(?,?,?,?,?)`, deviceID, credentialID, "linux", f.now, f.now); err != nil {
		t.Fatal(err)
	}
	return deviceID
}

func (f *serviceFixture) setAgentOnline(t *testing.T, deviceID string, online bool) {
	t.Helper()
	var lastSeen any
	if online {
		lastSeen = f.now
	}
	if _, err := f.db.ExecContext(context.Background(), "UPDATE device_agent SET last_seen_at = ? WHERE device_id = ?", lastSeen, deviceID); err != nil {
		t.Fatal(err)
	}
}

func (f *serviceFixture) auditRows(t *testing.T, kind string) []map[string]any {
	t.Helper()
	rows, err := f.db.QueryContext(context.Background(), "SELECT payload_json FROM audit_log WHERE source = ? AND kind = ? ORDER BY id", auditSourceSharing, kind)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var payloads []map[string]any
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
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
