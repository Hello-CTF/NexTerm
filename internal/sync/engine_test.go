package sync

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"github.com/Hello-CTF/NexTerm/internal/vault"
	"golang.org/x/crypto/argon2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// testSyncServer is a minimal faithful replica of the M116 account routes + v2 sync routes, for end-to-end tests.
type testSyncServer struct {
	*httptest.Server
	db       *store.Store
	accounts *account.Accounts
	logins   atomic.Int64

	failNextPushWith409 bool
	dropObjectID        string
	afterIDs            func()
}

func testCSRFToken(sessionID string) string {
	mac := hmac.New(sha256.New, []byte(sessionID))
	mac.Write([]byte("nexterm-csrf-v1"))
	return hex.EncodeToString(mac.Sum(nil))
}

func newTestSyncServer(t *testing.T) *testSyncServer {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return newTestSyncServerOn(t, db)
}

func newTestSyncServerOn(t *testing.T, db *store.Store) *testSyncServer {
	t.Helper()
	server := &testSyncServer{db: db, accounts: account.New(db.GORM())}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", server.serveLogin)
	mux.Handle("GET /auth/me", server.requireSession(http.HandlerFunc(server.serveMe)))
	mux.Handle("GET /auth/dek", server.requireSession(http.HandlerFunc(server.serveDEK)))
	mux.Handle("POST /sync/v2/push", server.requireSession(server.requireCSRF(server.maybeFailPush(NewObjectHandler(db.GORM(), nil)))))
	mux.Handle("POST /sync/v2/delete", server.requireSession(server.requireCSRF(NewObjectHandler(db.GORM(), nil))))
	mux.Handle("POST /sync/v2/pull", server.requireSession(NewObjectHandler(db.GORM(), nil)))
	mux.Handle("POST /sync/v2/ids", server.requireSession(server.maybePostIDs(NewObjectHandler(db.GORM(), nil))))
	server.Server = httptest.NewServer(mux)
	t.Cleanup(server.Server.Close)
	return server
}

// maybePostIDs runs a one-shot action after the ids service (drop an object row / concurrent update) to construct reconciliation edge scenarios.
func (s *testSyncServer) maybePostIDs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if s.dropObjectID != "" {
			id := s.dropObjectID
			s.dropObjectID = ""
			_, _ = s.db.DB().ExecContext(r.Context(), "DELETE FROM user_sync_object WHERE id = ?", id)
		}
		if s.afterIDs != nil {
			callback := s.afterIDs
			s.afterIDs = nil
			callback()
		}
	})
}

// maybeFailPush makes the next push return 409, for verifying the engine's real conflict retry path.
func (s *testSyncServer) maybeFailPush(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.failNextPushWith409 {
			s.failNextPushWith409 = false
			writeSyncError(w, http.StatusConflict, ipc.NewError(ipc.CodeForbidden, "服务端同步状态已变化：其他设备已更新或回滚了数据，请先拉取并合并，再推送本机变更"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *testSyncServer) serveLogin(w http.ResponseWriter, r *http.Request) {
	s.logins.Add(1)
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	user, err := s.accounts.Authenticate(r.Context(), request.Username, request.Password)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	token, session, err := s.accounts.IssueSession(r.Context(), user.ID, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/"})
	writeSyncJSON(w, http.StatusOK, map[string]any{
		"user":       map[string]string{"id": user.ID, "username": user.Username},
		"csrf_token": testCSRFToken(session.ID),
	})
}

func (s *testSyncServer) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			writeSyncError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, "会话无效或缺失"))
			return
		}
		identity, err := s.accounts.ValidateSession(r.Context(), cookie.Value)
		if err != nil {
			writeSyncError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, "会话无效或缺失"))
			return
		}
		ctx := context.WithValue(r.Context(), testIdentityKey{}, identity)
		next.ServeHTTP(w, r.WithContext(WithUserID(ctx, identity.UserID)))
	})
}

type testIdentityKey struct{}

func (s *testSyncServer) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, _ := r.Context().Value(testIdentityKey{}).(*account.Identity)
		if identity == nil || r.Header.Get(csrfHeaderName) != testCSRFToken(identity.SessionID) {
			writeSyncError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "CSRF 校验失败"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *testSyncServer) serveMe(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserIDFromContext(r.Context())
	user, err := s.accounts.GetUser(r.Context(), userID)
	if err != nil {
		writeSyncError(w, http.StatusInternalServerError, err)
		return
	}
	identity, _ := r.Context().Value(testIdentityKey{}).(*account.Identity)
	writeSyncJSON(w, http.StatusOK, map[string]any{
		"user":       map[string]string{"id": user.ID, "username": user.Username},
		"csrf_token": testCSRFToken(identity.SessionID),
	})
}

func (s *testSyncServer) serveDEK(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserIDFromContext(r.Context())
	envelopes, err := s.accounts.GetUserDEKEnvelopes(r.Context(), userID)
	if err != nil {
		writeSyncError(w, http.StatusInternalServerError, err)
		return
	}
	writeSyncJSON(w, http.StatusOK, map[string]any{
		"dek_envelope": envelopes.DEKEnvelope, "kdf_salt": envelopes.KDFSalt, "kdf_params": envelopes.KDFParams,
		"recovery_envelope": envelopes.RecoveryEnvelope, "recovery_hash": envelopes.RecoveryHash,
	})
}

func (s *testSyncServer) createUser(t *testing.T, username, password string) []byte {
	_, dek := seedTestSyncUser(t, s.db, username, password)
	return dek
}

func seedTestSyncUser(t *testing.T, db *store.Store, username, password string) (string, []byte) {
	t.Helper()
	// Sync tests exercise login and DEK unwrapping; account creation is covered
	// in the account package, so seed the same rows with a valid cheaper hash.
	dek, envelopes := newTestUserDEKEnvelopes(t, password)
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		t.Fatal(err)
	}
	key := argon2.IDKey([]byte(password), salt, 1, 8*1024, 1, 32)
	hash := fmt.Sprintf("$argon2id$v=19$m=8192,t=1,p=1$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
	clear(key)
	ctx := context.Background()
	userID, now := ids.New(), ids.NowMS()
	err := db.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := query.Use(tx)
		if err := q.AppUser.WithContext(ctx).Create(&store.AppUser{
			ID: userID, Username: username, DisplayName: username, Role: string(account.RoleUser),
			PasswordHash: hash, State: string(account.StateActive), CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		return q.UserDEK.WithContext(ctx).Create(&store.UserDEK{
			UserID: userID, DEKEnvelope: envelopes.DEKEnvelope, KDFSalt: envelopes.KDFSalt,
			KDFParams: envelopes.KDFParams, RecoveryEnvelope: envelopes.RecoveryEnvelope,
			RecoveryHash: envelopes.RecoveryHash, CreatedAt: now, UpdatedAt: now,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return userID, dek
}

func newTestUserDEKEnvelopes(t *testing.T, password string) ([]byte, *vault.UserDEKEnvelopes) {
	t.Helper()
	dek := vault.GenerateUserDEK()
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		t.Fatal(err)
	}
	key := argon2.IDKey([]byte(password), salt, 1, 8*1024, 1, 32)
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	recoveryKey, err := vault.GenerateRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	recoveryEnvelope, err := vault.WrapUserDEKWithRecovery(recoveryKey, dek)
	if err != nil {
		t.Fatal(err)
	}
	envelopes := &vault.UserDEKEnvelopes{
		DEKEnvelope:      aead.Seal(nonce, nonce, dek, []byte("nexterm/go/user-dek/v1")),
		KDFSalt:          salt,
		KDFParams:        `{"t":1,"m":8192,"p":1}`,
		RecoveryEnvelope: recoveryEnvelope,
		RecoveryHash:     vault.RecoveryKeyHash(recoveryKey),
	}
	opened, err := vault.UnwrapUserDEK(password, envelopes.DEKEnvelope, envelopes.KDFSalt, envelopes.KDFParams)
	if err != nil || !bytes.Equal(opened, dek) {
		t.Fatalf("test DEK envelope roundtrip: %v", err)
	}
	clear(opened)
	return dek, envelopes
}

type testDevice struct {
	db     *store.Store
	vault  *vault.Vault
	engine *Engine
}

func newTestDevice(t *testing.T) *testDevice {
	return newTestDeviceWithMaster(t, false)
}

func newMasterTestDevice(t *testing.T) *testDevice {
	return newTestDeviceWithMaster(t, true)
}

func newTestDeviceWithMaster(t *testing.T, master bool) *testDevice {
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
	password := ""
	if master {
		password = "device-master-pw"
	}
	credentialVault := newSyncTestVault(t, db, password)
	return &testDevice{db: db, vault: credentialVault, engine: NewEngine(db, credentialVault, nil)}
}

func (d *testDevice) config(server *testSyncServer, username, password string) RemoteConfig {
	return RemoteConfig{URL: server.URL, Username: username, Password: password}
}

func putDeviceGroup(t *testing.T, device *testDevice, id string, parentID *string, name string, updatedAt int64) {
	t.Helper()
	if _, err := device.db.GroupUpsert(context.Background(), id, parentID, name, 0, 1, updatedAt); err != nil {
		t.Fatal(err)
	}
}

func putDeviceAsset(t *testing.T, device *testDevice, row store.AssetRow) {
	t.Helper()
	if _, err := device.db.AssetUpsert(context.Background(), row); err != nil {
		t.Fatal(err)
	}
}

func putDeviceCredential(t *testing.T, device *testDevice, id, name, secret string, updatedAt int64) {
	t.Helper()
	ctx := context.Background()
	nonce, blob, err := device.vault.EncryptCredential(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := device.db.CredentialPut(ctx, store.CredentialInput{
		ID: id, Name: name, Kind: "password", Nonce: nonce, Blob: blob, KEKHint: device.vault.KEKHint(),
	}); err != nil {
		t.Fatal(err)
	}
}

func putDeviceSnippet(t *testing.T, device *testDevice, id, name, body string, updatedAt int64) {
	t.Helper()
	if err := device.db.GORM().WithContext(context.Background()).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"name", "body", "updated_at"}),
	}).Create(&model.SnippetRow{
		ID: id, Name: name, Body: body, Sort: 0, CreatedAt: 1, UpdatedAt: updatedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func putDeviceTranscript(t *testing.T, device *testDevice, id string, startedAt int64, endedAt *int64, content []byte, syncOptIn bool) {
	t.Helper()
	ctx := context.Background()
	row := store.TranscriptRow{
		ID: id, SessionID: ids.New(), AssetID: "asset-1", AssetName: "web-01", AssetKind: "ssh", StartedAt: startedAt,
	}
	if err := device.db.TranscriptStart(ctx, row); err != nil {
		t.Fatal(err)
	}
	if content != nil {
		if err := device.db.TranscriptAppendChunks(ctx, id, []store.TranscriptChunkRow{
			{Seq: 0, TabID: "tab-1", TS: startedAt, Data: content},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if endedAt != nil {
		if err := device.db.TranscriptEnd(ctx, id, *endedAt, false); err != nil {
			t.Fatal(err)
		}
	}
	if syncOptIn && endedAt != nil {
		if err := device.db.TranscriptSetSyncOptIn(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
}

func syncDevice(t *testing.T, device *testDevice, server *testSyncServer, username, password string) SyncReport {
	t.Helper()
	report, err := device.engine.Sync(context.Background(), device.config(server, username, password))
	if err != nil {
		t.Fatalf("sync: %v (warnings=%v)", err, report.Warnings)
	}
	return report
}

func deviceAssetNames(t *testing.T, device *testDevice) map[string]string {
	t.Helper()
	assets, err := device.db.AssetList(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, asset := range assets {
		names[asset.ID] = asset.Name
	}
	return names
}

func containsFold(haystack []byte, needle string) bool {
	return strings.Contains(strings.ToLower(string(haystack)), strings.ToLower(needle))
}
