//go:build android

package biometric

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Android 实现复用 wails 移动桥: 解锁材料(凭据库主密码)存
// EncryptedSharedPreferences(Android Keystore 主密钥加密), 验证走
// BiometricPrompt(生物识别或锁屏凭据), 结果经 "common:biometric" 事件回流。

const biometricEvent = "common:biometric"

const authenticateTimeout = 2 * time.Minute

var authenticateMu sync.Mutex

func Current() (Status, error) {
	_, found, err := application.Android.SecureGet(materialAccount)
	if err != nil {
		// Keystore/加密存储初始化失败视为能力不可用, 不阻塞密码解锁路径
		return Status{Available: false}, nil
	}
	return Status{Available: true, CanEvaluate: true, HasMaterial: found}, nil
}

func Unlock() (string, error) {
	value, found, err := application.Android.SecureGet(materialAccount)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrNoMaterial
	}
	if err := authenticate(evaluateReason); err != nil {
		return "", err
	}
	return value, nil
}

func Enable(password string) error {
	if password == "" {
		return errors.New("解锁材料为空")
	}
	return application.Android.SecureSet(materialAccount, password)
}

func Disable() error {
	return application.Android.SecureDelete(materialAccount)
}

// authenticate 弹出系统 BiometricPrompt 并同步等待结果: 桥接是异步事件,
// 这里注册一次性监听后以 channel 回收, 互斥保证同时只有一个弹窗在途。
func authenticate(reason string) error {
	authenticateMu.Lock()
	defer authenticateMu.Unlock()
	app := application.Get()
	if app == nil {
		return ErrUnavailable
	}
	result := make(chan error, 1)
	cancel := app.Event.On(biometricEvent, func(event *application.CustomEvent) {
		data, _ := event.Data.(map[string]any)
		if ok, _ := data["ok"].(bool); ok {
			select {
			case result <- nil:
			default:
			}
			return
		}
		message, _ := data["error"].(string)
		select {
		case result <- errors.New(authenticateErrorMessage(message)):
		default:
		}
	})
	defer cancel()
	application.Android.BiometricAuthenticate(reason)
	select {
	case err := <-result:
		return err
	case <-time.After(authenticateTimeout):
		return errors.New("生物识别验证超时，请重试")
	}
}

func authenticateErrorMessage(raw string) string {
	if raw == "" {
		return "生物识别验证未通过"
	}
	if strings.Contains(raw, "no biometrics or device credential") {
		return "设备未录入生物识别或锁屏密码，请使用密码解锁"
	}
	return raw
}
