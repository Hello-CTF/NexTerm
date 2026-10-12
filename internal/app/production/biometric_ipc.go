package production

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/biometric"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

type biometricUnlockResult struct {
	OK       bool   `json:"ok"`
	Password string `json:"password,omitempty"`
	Message  string `json:"message,omitempty"`
}

type biometricEnableRequest struct {
	Password string `json:"password"`
}

// biometricBackend 隔离平台相关实现, 测试以假实现替换。
type biometricBackend interface {
	Current() (biometric.Status, error)
	Unlock() (string, error)
	Enable(password string) error
	Disable() error
}

type platformBiometric struct{}

func (platformBiometric) Current() (biometric.Status, error) { return biometric.Current() }
func (platformBiometric) Unlock() (string, error)            { return biometric.Unlock() }
func (platformBiometric) Enable(password string) error       { return biometric.Enable(password) }
func (platformBiometric) Disable() error                     { return biometric.Disable() }

// enable 前要求凭据库处于解锁态, 避免任何已认证客户端向 Keychain 植入错误材料。
type vaultUnlockState interface {
	Status() vault.Status
}

func registerBiometricCommands(dispatcher *ipc.Dispatcher, backend biometricBackend, vaultState vaultUnlockState) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "biometric_status", func(_ context.Context, _ *ipc.Call, _ struct{}) (biometric.Status, error) {
				return backend.Current()
			})
		},
		func() error {
			// 取消/失败不视为系统错误: ok=false 带消息, 前端保持密码输入。
			return ipc.Register(dispatcher, "biometric_unlock", func(_ context.Context, _ *ipc.Call, _ struct{}) (biometricUnlockResult, error) {
				password, err := backend.Unlock()
				if err != nil {
					return biometricUnlockResult{Message: err.Error()}, nil
				}
				return biometricUnlockResult{OK: true, Password: password}, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "biometric_enable", func(_ context.Context, _ *ipc.Call, input biometricEnableRequest) (any, error) {
				if input.Password == "" {
					return nil, ipc.BadParam(errors.New("解锁材料为空"))
				}
				if vaultState == nil || !vaultState.Status().Unlocked {
					return nil, errors.New("凭据库未解锁, 请先解锁后再启用生物识别")
				}
				return nil, backend.Enable(input.Password)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "biometric_disable", func(_ context.Context, _ *ipc.Call, _ struct{}) (any, error) {
				return nil, backend.Disable()
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}
