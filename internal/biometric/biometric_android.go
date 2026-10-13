//go:build android

package biometric

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// The Android implementation reuses the wails mobile bridge: the unlock material (credential vault master password) is stored
// in EncryptedSharedPreferences (encrypted with the Android Keystore master key), verification goes through
// BiometricPrompt (biometrics or lock screen credential), and results flow back via the "common:biometric" event.

const biometricEvent = "common:biometric"

const authenticateTimeout = 2 * time.Minute

var authenticateMu sync.Mutex

func Current() (Status, error) {
	_, found, err := application.Android.SecureGet(materialAccount)
	if err != nil {
		// Keystore/encrypted storage init failure counts as capability unavailable; the password unlock path is not blocked
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

// authenticate shows the system BiometricPrompt and waits synchronously for the result: the bridge is an async event,
// so a one-shot listener is registered here and the result is collected via a channel; the mutex guarantees only one prompt is in flight.
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
