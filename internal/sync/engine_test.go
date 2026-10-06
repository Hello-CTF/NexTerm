package sync

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

// testSyncServer 是 M116 账号路由 + v2 同步路由的最小 faithful 复刻, 供端到端测试使用。
type testSyncServer struct {
	*httptest.Server
	db       *store.Store
	accounts *account.Accounts
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
	server := &testSyncServer{db: db, accounts: account.New(db.DB())}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", server.serveLogin)
	mux.Handle("GET /auth/me", server.requireSession(http.HandlerFunc(server.serveMe)))
	mux.Handle("GET /auth/dek", server.requireSession(http.HandlerFunc(server.serveDEK)))
	mux.Handle("POST /sync/v2/push", server.requireSession(server.requireCSRF(NewObjectHandler(db.DB()))))
	mux.Handle("POST /sync/v2/pull", server.requireSession(NewObjectHandler(db.DB())))
	mux.Handle("POST /sync/v2/ids", server.requireSession(NewObjectHandler(db.DB())))
	server.Server = httptest.NewServer(mux)
	t.Cleanup(server.Server.Close)
	return server
}

func (s *testSyncServer) serveLogin(w http.ResponseWriter, r *http.Request) {
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
	t.Helper()
	dek, envelopes, _, err := vault.GenerateUserDEKEnvelopes(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.accounts.CreateUserWithEnvelopes(context.Background(), username, username, password, envelopes); err != nil {
		t.Fatal(err)
	}
	return dek
}

type testDevice struct {
	db     *store.Store
	vault  *vault.Vault
	engine *Engine
}

func newTestDevice(t *testing.T) *testDevice {
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
	credentialVault := vault.Load(ctx, db)
	if err := credentialVault.InitMaster(ctx, "device-master-pw"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(credentialVault.Lock)
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
	if _, err := device.db.DB().ExecContext(context.Background(),
		"INSERT INTO snippet(id, group_id, name, body, sort, created_at, updated_at) VALUES(?,NULL,?,?,0,?,?)",
		id, name, body, 1, updatedAt); err != nil {
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
