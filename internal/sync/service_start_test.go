package sync

import (
	"context"
	"net/http"
	"net/http/httptest"
	stdsync "sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// probeEventRecorder 记录启动探测与后台轮发出的 sync 状态事件。
type probeEventRecorder struct {
	mu     stdsync.Mutex
	events []ipc.Event
}

func (r *probeEventRecorder) emit(_ context.Context, event ipc.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *probeEventRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func openLinkStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.AssetEnsureBuiltinLocal(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedSavedLink 以指定主密码初始化凭据库并保存一份完整链接, 模拟上一次运行留下的配置。
func seedSavedLink(t *testing.T, db *store.Store, masterPassword, url, username, password string) {
	t.Helper()
	ctx := context.Background()
	seedVault := vault.Load(ctx, db)
	if err := seedVault.InitMaster(ctx, masterPassword); err != nil {
		t.Fatal(err)
	}
	seed := New(db, seedVault, WithMetadata("test", true))
	if _, err := seed.LinkSet(ctx, LinkPatch{
		URL: testPtr(url), Username: testPtr(username), Password: testPtr(password),
	}); err != nil {
		t.Fatal(err)
	}
}

// restartService 在同一存储上重新 Load 凭据库并新建 Service, 模拟桌面端进程重启:
// 新 Engine 的会话缓存为空, 链接配置从 settings 重新解密读出。
func restartService(t *testing.T, db *store.Store, events *probeEventRecorder) (*Service, *vault.Vault) {
	t.Helper()
	reloaded := vault.Load(context.Background(), db)
	service := New(db, reloaded, WithMetadata("test", true), WithEvents(ipc.EmitterFunc(events.emit)))
	service.syncPeriod = time.Hour
	return service, reloaded
}

func startProbeService(t *testing.T, service *Service) {
	t.Helper()
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
}

func waitForCondition(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func linkOf(t *testing.T, service *Service) Link {
	t.Helper()
	link, err := service.LinkGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return link
}

func TestServiceStartAutoConnectsSavedLink(t *testing.T) {
	ctx := context.Background()
	db := openLinkStore(t)
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-password")
	seedSavedLink(t, db, "", server.URL, "alice", "alice-password")

	events := &probeEventRecorder{}
	service, reloaded := restartService(t, db, events)
	if !reloaded.Status().Unlocked {
		t.Fatal("passwordless vault should auto-unlock on reload")
	}
	startProbeService(t, service)

	waitForCondition(t, "saved link not auto-connected after restart", func() bool {
		status, err := service.Status(ctx)
		return err == nil && status.LoggedIn
	})
	waitForCondition(t, "startup probe did not broadcast sync status", func() bool {
		return events.count() > 0
	})
	if link := linkOf(t, service); link.LastError != "" {
		t.Fatalf("unexpected last error after auto-connect: %q", link.LastError)
	}
}

func TestServiceStartProbeFailureRecordsLastError(t *testing.T) {
	db := openLinkStore(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	seedSavedLink(t, db, "", deadURL, "alice", "alice-password")

	events := &probeEventRecorder{}
	service, _ := restartService(t, db, events)
	startProbeService(t, service)

	// 只有启动探测会在此场景发出事件(会话永不建立, 后台轮无变更不广播)。
	waitForCondition(t, "startup probe did not run", func() bool {
		return events.count() > 0
	})
	link := linkOf(t, service)
	if !link.IsConfigured() || link.LastError == "" {
		t.Fatalf("link = %+v, want configured with recorded probe error", link)
	}
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.LoggedIn {
		t.Fatal("logged in against a dead server")
	}
}

func TestServiceStartProbeMFARequiredIsHonest(t *testing.T) {
	db := openLinkStore(t)
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	fixture.bindTOTP(t, "alice", "alice-pw-123")
	seedSavedLink(t, db, "", fixture.URL, "alice", "alice-pw-123")

	events := &probeEventRecorder{}
	service, _ := restartService(t, db, events)
	startProbeService(t, service)

	waitForCondition(t, "MFA account not flagged for interactive re-login", func() bool {
		link, err := service.LinkGet(context.Background())
		return err == nil && link.LastError == errReloginMFA.Error()
	})
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.LoggedIn || !status.Configured {
		t.Fatalf("status = %+v, want configured but not logged in", status)
	}
	waitForCondition(t, "startup probe did not broadcast sync status", func() bool {
		return events.count() > 0
	})
}

func TestServiceStartProbeResetRequiredIsHonest(t *testing.T) {
	db := openLinkStore(t)
	fixture := newRemoteAPIFixture(t)
	userID := fixture.createUser(t, "alice", "temp-pw-123")
	if err := fixture.accounts.AdminResetUser(context.Background(), userID); err != nil {
		t.Fatal(err)
	}
	seedSavedLink(t, db, "", fixture.URL, "alice", "temp-pw-123")

	events := &probeEventRecorder{}
	service, _ := restartService(t, db, events)
	startProbeService(t, service)

	waitForCondition(t, "reset-required account not flagged for interactive re-login", func() bool {
		link, err := service.LinkGet(context.Background())
		return err == nil && link.LastError == errReloginReset.Error()
	})
	if status, err := service.Status(context.Background()); err != nil || status.LoggedIn {
		t.Fatalf("status = %+v, %v, want not logged in", status, err)
	}
}

func TestServiceStartConnectsAfterVaultUnlock(t *testing.T) {
	ctx := context.Background()
	db := openLinkStore(t)
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-password")
	seedSavedLink(t, db, "master-pw-123", server.URL, "alice", "alice-password")

	events := &probeEventRecorder{}
	service, reloaded := restartService(t, db, events)
	if reloaded.Status().Unlocked {
		t.Fatal("master-password vault must stay locked after reload")
	}
	startProbeService(t, service)

	// 锁定期内探测与后台轮都静默跳过: 不发事件、不记错误。
	time.Sleep(100 * time.Millisecond)
	if events.count() != 0 {
		t.Fatal("probe must stay silent while the vault is locked")
	}
	if link, err := service.LinkGet(context.Background()); err == nil || link.LastError != "" {
		t.Fatalf("locked vault must not record probe state: %+v, %v", link, err)
	}

	if err := reloaded.UnlockMaster(ctx, "master-pw-123"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, "saved link not connected after vault unlock", func() bool {
		status, err := service.Status(ctx)
		return err == nil && status.LoggedIn
	})
	waitForCondition(t, "unlock-triggered connect did not broadcast sync status", func() bool {
		return events.count() > 0
	})
}
