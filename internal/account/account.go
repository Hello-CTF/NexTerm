package account

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

type Accounts struct {
	db                       *gorm.DB
	now                      func() int64
	totpKeyPath              string
	activeSuperadminListener func(context.Context)
	writeMu                  sync.Mutex
	epochs                   *epochTracker
	// revocationMu is the shared async-boundary mutex: session issuance,
	// revocation, password changes, resets, disables, device revocations, and
	// the TOTP/DEK/MFA generation checks all serialize on it, so an epoch bump
	// cannot land inside another boundary's check-to-commit window.
	revocationMu sync.Mutex
	// blockedMu guards blockedListeners, called after an account is disabled or reset so dependents can drop live channels.
	blockedMu        sync.Mutex
	blockedListeners []func(userID string)
}

// userTable centralizes the account table name for future storage backend changes.
const userTable = "app_user"

type Option func(*Accounts)

func WithNow(now func() int64) Option {
	return func(a *Accounts) {
		if now != nil {
			a.now = now
		}
	}
}

func WithActiveSuperadminListener(listener func(context.Context)) Option {
	return func(a *Accounts) {
		a.activeSuperadminListener = listener
	}
}

// OnUserBlocked registers a callback fired after a user is disabled or marked reset-required,
// so services holding long-lived channels for that user (device control channels) can close them.
func (a *Accounts) OnUserBlocked(listener func(userID string)) {
	if listener == nil {
		return
	}
	a.blockedMu.Lock()
	a.blockedListeners = append(a.blockedListeners, listener)
	a.blockedMu.Unlock()
}

func (a *Accounts) notifyUserBlocked(userID string) {
	a.blockedMu.Lock()
	listeners := append([]func(string){}, a.blockedListeners...)
	a.blockedMu.Unlock()
	for _, listener := range listeners {
		listener(userID)
	}
}

func (a *Accounts) notifyActiveSuperadmin(ctx context.Context, user *User) {
	if a.activeSuperadminListener == nil || user == nil || user.Role != RoleSuperadmin || user.State != StateActive {
		return
	}
	a.activeSuperadminListener(ctx)
}

func New(db *gorm.DB, options ...Option) *Accounts {
	a := &Accounts{db: db, now: ids.NowMS, epochs: newEpochTracker()}
	for _, option := range options {
		option(a)
	}
	return a
}

func userFromModel(row *store.AppUser) *User {
	user := &User{
		ID: row.ID, Username: row.Username, DisplayName: row.DisplayName, Role: Role(row.Role), State: State(row.State),
		MustChangePassword: row.MustChangePassword, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, passwordHash: row.PasswordHash,
	}
	if row.LastLoginAt != nil {
		user.LastLoginAt = *row.LastLoginAt
	}
	return user
}

func dbError(err error) error {
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return err
	}
	return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
}

func translateUserWriteError(err error) error {
	if store.IsUniqueErr(err) {
		return ipc.NewError(ipc.CodeBadParam, "参数错误: 用户名已存在")
	}
	return dbError(err)
}

func (a *Accounts) withBusyRetry(ctx context.Context, fn func() error) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := fn()
		if !store.IsBusy(err) {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
