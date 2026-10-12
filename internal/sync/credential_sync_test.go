package sync

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	stdsync "sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func serverObjectIDs(t *testing.T, server *testSyncServer) map[string]bool {
	t.Helper()
	rows, err := server.db.DB().QueryContext(context.Background(), "SELECT id FROM user_sync_object")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		set[id] = true
	}
	return set
}

func serverAssetCredID(t *testing.T, server *testSyncServer, username, password, assetID string) *string {
	t.Helper()
	var blob []byte
	if err := server.db.DB().QueryRowContext(context.Background(),
		"SELECT blob FROM user_sync_object WHERE id = ?", assetID).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	kind, plaintext, err := tryOpenObject(mustDEK(t, server, username, password), blob, assetID)
	if err != nil || kind != KindAsset {
		t.Fatalf("server asset %s kind=%q err=%v", assetID, kind, err)
	}
	var payload assetObject
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.CredID
}

func warningsContain(warnings []string, substr string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substr) {
			return true
		}
	}
	return false
}

// 凭据库锁定只延迟凭据, 不再整轮失败: 资产/片段照常推送; 对端凭据未到时降级应用(清引用)
// 但不记对账也不回推降级版本, 解锁补推凭据后对端自动恢复引用。
func TestEngineCredentialDeferredWhileVaultLocked(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	credentialID := ids.New()
	putDeviceCredential(t, deviceA, credentialID, "root 密码", "s3cret", 100)
	assetID := ids.New()
	putDeviceAsset(t, deviceA, store.AssetRow{
		ID: assetID, Kind: "ssh", Name: "web-01", CredID: &credentialID, CreatedAt: 100, UpdatedAt: 100,
	})
	snippetID := ids.New()
	putDeviceSnippet(t, deviceA, snippetID, "部署", "systemctl restart app", 100)

	deviceA.vault.Lock()
	report, err := deviceA.engine.Sync(ctx, deviceA.config(server, "alice", "alice-pw-123"))
	if err != nil {
		t.Fatalf("locked vault must not fail the round: %v", err)
	}
	if !warningsContain(report.Warnings, "凭据") {
		t.Fatalf("locked round must warn about deferred credentials: %+v", report)
	}
	objects := serverObjectIDs(t, server)
	if objects[credentialID] {
		t.Fatal("credential must not be pushed while vault locked")
	}
	if !objects[assetID] || !objects[snippetID] {
		t.Fatalf("asset/snippet must sync while vault locked: %v", objects)
	}

	reportB := syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if !warningsContain(reportB.Warnings, "尚未同步到本机") {
		t.Fatalf("deviceB must warn about the not-yet-arrived credential: %+v", reportB)
	}
	asset, err := deviceB.db.AssetGet(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	if asset.CredID != nil {
		t.Fatalf("deviceB asset cred reference must be cleared while credential missing: %+v", asset.CredID)
	}
	if _, err := deviceB.db.CredentialGetRow(ctx, credentialID); !isNotFound(err) {
		t.Fatalf("credential must not exist on deviceB yet: %v", err)
	}
	// 降级形态不得回推: 云端资产对象必须保持带引用的原始版本。
	if credID := serverAssetCredID(t, server, "alice", "alice-pw-123", assetID); credID == nil || *credID != credentialID {
		t.Fatalf("server asset must keep the pristine cred reference: %+v", credID)
	}

	if err := deviceA.vault.UnlockMaster(ctx, "device-master-pw"); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if !serverObjectIDs(t, server)[credentialID] {
		t.Fatal("credential must be pushed after unlock")
	}
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	row, err := deviceB.db.CredentialGetRow(ctx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := deviceB.vault.DecryptCredentialString(ctx, row)
	if err != nil || secret != "s3cret" {
		t.Fatalf("deviceB secret=%q err=%v", secret, err)
	}
	asset, err = deviceB.db.AssetGet(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	if asset.CredID == nil || *asset.CredID != credentialID {
		t.Fatalf("deviceB asset cred reference must be restored after credential arrived: %+v", asset.CredID)
	}

	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
}

// 拉取侧: 凭据对象在凭据库锁定时不应用也不记对账, 解锁后下一轮自动重拉应用;
// 资产先到时降级清除引用, 凭据到达后自动恢复。
func TestEngineCredentialPullDeferredWhileVaultLocked(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	credentialID := ids.New()
	putDeviceCredential(t, deviceA, credentialID, "root 密码", "s3cret-拉取", 100)
	assetID := ids.New()
	putDeviceAsset(t, deviceA, store.AssetRow{
		ID: assetID, Kind: "ssh", Name: "db-01", CredID: &credentialID, CreatedAt: 100, UpdatedAt: 100,
	})
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")

	deviceB.vault.Lock()
	report, err := deviceB.engine.Sync(ctx, deviceB.config(server, "alice", "alice-pw-123"))
	if err != nil {
		t.Fatalf("locked vault must not fail the round: %v", err)
	}
	if !warningsContain(report.Warnings, "暂缓应用") {
		t.Fatalf("locked pull must warn about the deferred credential: %+v", report)
	}
	if _, err := deviceB.db.CredentialGetRow(ctx, credentialID); !isNotFound(err) {
		t.Fatalf("credential must not be applied while vault locked: %v", err)
	}
	asset, err := deviceB.db.AssetGet(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	if asset.CredID != nil {
		t.Fatalf("deviceB asset cred reference must be cleared while credential missing: %+v", asset.CredID)
	}

	if err := deviceB.vault.UnlockMaster(ctx, "device-master-pw"); err != nil {
		t.Fatal(err)
	}
	report = syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	row, err := deviceB.db.CredentialGetRow(ctx, credentialID)
	if err != nil {
		t.Fatalf("deferred credential was never retried after unlock (report=%+v)", report)
	}
	secret, err := deviceB.vault.DecryptCredentialString(ctx, row)
	if err != nil || secret != "s3cret-拉取" {
		t.Fatalf("deviceB secret=%q err=%v", secret, err)
	}
	// 补拉顺序不定(凭据与资产同批补拉): 再同步一轮, 资产引用必然恢复。
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	asset, err = deviceB.db.AssetGet(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	if asset.CredID == nil || *asset.CredID != credentialID {
		t.Fatalf("deviceB asset cred reference must be restored after credential arrived: %+v", asset.CredID)
	}

	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
}

// 浏览器明文应用路径: 凭据未到时降级清除引用(外键约束不允许悬空), 凭据到达后
// 重应用同一资产经降级形态判定自动恢复引用, 之后逐字节一致。
func TestApplyAssetRestoresCredentialReference(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()
	credentialID := ids.New()
	assetID := ids.New()

	asset := ApplyObject{ID: assetID, Kind: KindAsset, Payload: applyPayload(t, assetObject{
		ID: assetID, Kind: "ssh", Name: "web-01", CredID: &credentialID,
		OptionsJSON: "{}", CreatedAt: 100, UpdatedAt: 100,
	})}
	result := mustApplyObjects(t, instance, asset)
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	if !strings.Contains(result.Objects[0].Warning, "已暂时清除该引用") {
		t.Fatalf("warning=%q", result.Objects[0].Warning)
	}
	row, err := instance.db.AssetGet(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	if row.CredID != nil {
		t.Fatalf("cred reference must be cleared while credential missing: %+v", row.CredID)
	}

	result = mustApplyObjects(t, instance, ApplyObject{ID: credentialID, Kind: KindCredential, Payload: applyPayload(t, credentialObject{
		ID: credentialID, Name: "凭据", Kind: "password", Secret: "s3cret", UpdatedAt: 100,
	})})
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)

	result = mustApplyObjects(t, instance, asset)
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	row, err = instance.db.AssetGet(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	if row.CredID == nil || *row.CredID != credentialID {
		t.Fatalf("cred reference must be restored after credential arrived: %+v", row.CredID)
	}

	result = mustApplyObjects(t, instance, asset)
	requireApplyResult(t, result.Objects[0], ApplyResultIdentical)
}

type recordCountingHandler struct {
	mu      stdsync.Mutex
	records []slog.Record
}

func (h *recordCountingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordCountingHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record)
	return nil
}

func (h *recordCountingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *recordCountingHandler) WithGroup(string) slog.Handler { return h }

func (h *recordCountingHandler) count(message string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, record := range h.records {
		if strings.Contains(record.Message, message) {
			count++
		}
	}
	return count
}

// 凭据库锁定导致后台同步暂停时: 轮次状态如实记录(此前静默跳过), 每段连续暂停期只记一行日志,
// 解锁恢复后再锁定重新记一行。
func TestSyncBackgroundNotesVaultBlocked(t *testing.T) {
	handler := &recordCountingHandler{}
	instance := newTestInstance(t, true, WithLogger(slog.New(handler)))
	ctx := context.Background()
	url, username, password := "http://127.0.0.1:1", "alice", "alice-pw-123"
	if _, err := instance.service.LinkSet(ctx, LinkPatch{URL: &url, Username: &username, Password: &password}); err != nil {
		t.Fatal(err)
	}

	instance.vault.Lock()
	instance.service.syncBackground(ctx)
	instance.service.roundMu.RLock()
	lastError := instance.service.roundStatus.LastError
	lastAttempt := instance.service.roundStatus.LastAttemptAt
	instance.service.roundMu.RUnlock()
	if !strings.Contains(lastError, "凭据库已锁定") {
		t.Fatalf("blocked round must record lastError, got %q", lastError)
	}
	if lastAttempt.IsZero() {
		t.Fatal("blocked round must record lastAttemptAt")
	}

	instance.service.syncBackground(ctx)
	if got := handler.count("background sync paused"); got != 1 {
		t.Fatalf("continuous pause must log once, got %d", got)
	}

	if err := instance.vault.UnlockMaster(ctx, "sync-test-master"); err != nil {
		t.Fatal(err)
	}
	instance.service.syncBackground(ctx)

	instance.vault.Lock()
	instance.service.syncBackground(ctx)
	if got := handler.count("background sync paused"); got != 2 {
		t.Fatalf("pause after recovery must log again, got %d", got)
	}
}
