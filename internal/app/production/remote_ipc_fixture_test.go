package production

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/server"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// remoteTestAccounts 为远程代理 IPC 测试提供真实账号面: 会话签发/吊销、TOTP、
// 强制改密全部走真实 account.Accounts + 真实 internal/server 账号路由;
// fleet/share 路由是外层 mux 上的 canned 复刻(契约保真由 internal/sync 的
// remote_api_test 与 internal/server 自身测试共同覆盖)。
type remoteTestAccounts struct {
	accounts *account.Accounts
}

func newRemoteTestAccounts(t *testing.T, db *store.Store) *remoteTestAccounts {
	t.Helper()
	return &remoteTestAccounts{accounts: account.New(db.GORM(), account.WithTOTPKeyFile(filepath.Join(t.TempDir(), "totp.key")))}
}

func (f *remoteTestAccounts) createVerifiedUser(ctx context.Context, username, password string) error {
	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes(password)
	if err != nil {
		return err
	}
	_, err = f.accounts.CreateUserWithEnvelopes(ctx, username, username, password, envelopes)
	return err
}

func (f *remoteTestAccounts) bindTOTP(t *testing.T, username, password string) string {
	t.Helper()
	ctx := context.Background()
	user, err := f.accounts.Authenticate(ctx, username, password)
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := f.accounts.BeginTOTPSetup(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.accounts.ConfirmTOTPSetup(ctx, user.ID, remoteTestTOTPCode(t, secret, time.Now())); err != nil {
		t.Fatal(err)
	}
	return secret
}

func (f *remoteTestAccounts) totpCode(t *testing.T, secret string) string {
	t.Helper()
	return remoteTestTOTPCode(t, secret, time.Now())
}

func (f *remoteTestAccounts) userID(ctx context.Context, username string) (string, error) {
	users, err := f.accounts.ListUsers(ctx)
	if err != nil {
		return "", err
	}
	for _, user := range users {
		if user.Username == username {
			return user.ID, nil
		}
	}
	return "", fmt.Errorf("user %s not found", username)
}

func (f *remoteTestAccounts) adminResetUser(ctx context.Context, username string) error {
	id, err := f.userID(ctx, username)
	if err != nil {
		return err
	}
	return f.accounts.AdminResetUser(ctx, id)
}

func (f *remoteTestAccounts) revokeAllSessions(ctx context.Context, username string) error {
	id, err := f.userID(ctx, username)
	if err != nil {
		return err
	}
	_, err = f.accounts.RevokeUserSessions(ctx, id, "")
	return err
}

func remoteTestTOTPCode(t *testing.T, secretBase32 string, at time.Time) string {
	t.Helper()
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretBase32)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha1.New, secret)
	var counterBytes [8]byte
	binary.BigEndian.PutUint64(counterBytes[:], uint64(at.Unix())/30)
	_, _ = mac.Write(counterBytes[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000)
}

// remoteTestCSRF 与 internal/server account_session.go 的派生算法一致。
func remoteTestCSRF(sessionID string) string {
	mac := hmac.New(sha256.New, []byte(sessionID))
	mac.Write([]byte("nexterm-csrf-v1"))
	return hex.EncodeToString(mac.Sum(nil))
}

type remoteTestIdentityKey struct{}

func remoteTestWriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func remoteTestWriteError(w http.ResponseWriter, status int, code ipc.Code, message string) {
	remoteTestWriteJSON(w, status, ipc.Failure(ipc.NewError(code, message)))
}

// newRemoteTestMux 返回真实 server.Handler() 之上叠加 canned fleet 路由的 mux。
func newRemoteTestMux(t *testing.T, f *remoteTestAccounts) http.Handler {
	t.Helper()
	realServer, err := server.New(server.Config{
		Options:    server.Options{Listen: "127.0.0.1:0", DataDir: t.TempDir()},
		Dispatcher: ipc.NewDispatcher(),
		Accounts:   f.accounts,
		Version:    "9.9.9-test",
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Channels: server.ChannelBinderFunc(func(string) (server.ChannelReceiver, error) {
			return nil, errors.New("no test channel")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = realServer.Close() })

	session := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie("nexterm_session")
			if err != nil || cookie.Value == "" {
				remoteTestWriteError(w, http.StatusUnauthorized, ipc.CodeForbidden, "会话无效或缺失，请重新登录")
				return
			}
			identity, err := f.accounts.ValidateSession(r.Context(), cookie.Value)
			if err != nil {
				remoteTestWriteError(w, http.StatusUnauthorized, ipc.CodeForbidden, "会话无效或缺失，请重新登录")
				return
			}
			if identity.State == account.StateResetRequired {
				remoteTestWriteError(w, http.StatusForbidden, ipc.CodeForbidden, "必须先完成密码重置")
				return
			}
			ctx := context.WithValue(r.Context(), remoteTestIdentityKey{}, identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	csrf := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, _ := r.Context().Value(remoteTestIdentityKey{}).(*account.Identity)
			if identity == nil || r.Header.Get("X-NexTerm-CSRF") != remoteTestCSRF(identity.SessionID) {
				remoteTestWriteError(w, http.StatusForbidden, ipc.CodeForbidden, "CSRF 校验失败：页面已过期，请刷新后重试")
				return
			}
			next.ServeHTTP(w, r)
		})
	}

	mux := http.NewServeMux()
	mux.Handle("GET /fleet/devices", session(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		remoteTestWriteJSON(w, http.StatusOK, map[string]any{"devices": []map[string]any{
			{"id": "d-1", "name": "edge-01", "kind": "agent", "state": "active", "created_at": 1, "last_seen_at": 2, "revoked_at": 0},
		}})
	})))
	mux.Handle("POST /fleet/devices/{id}/autostart", session(csrf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Desired bool `json:"desired"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			remoteTestWriteError(w, http.StatusBadRequest, ipc.CodeBadParam, "请求体不是合法 JSON")
			return
		}
		remoteTestWriteJSON(w, http.StatusOK, map[string]any{"ok": true, "desired_autostart": request.Desired})
	}))))
	mux.Handle("/", realServer.Handler())
	return mux
}
