//go:build ios

package biometric

/*
#cgo CFLAGS: -x objective-c -fmodules -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework LocalAuthentication -framework Security
#include <stdlib.h>
#include "biometric_ios.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

func cString(s string) (*C.char, func()) {
	c := C.CString(s)
	return c, func() { C.free(unsafe.Pointer(c)) }
}

func cStr(p *C.char) string {
	if p == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

// Current 汇总设备生物识别能力与 Keychain 材料状态。
func Current() (Status, error) {
	status := Status{Available: true, CanEvaluate: bool(C.nx_biometric_can_evaluate())}
	has, err := keychainHasMaterial()
	if err != nil {
		return Status{}, err
	}
	status.HasMaterial = has
	return status, nil
}

// Unlock 弹出系统面容/指纹验证, 通过后读取 Keychain 中的解锁材料。
// 用户取消或验证失败返回 error(消息可直接展示), 材料不会外泄。
func Unlock() (string, error) {
	if !bool(C.nx_biometric_can_evaluate()) {
		return "", ErrUnavailable
	}
	reason, free := cString(evaluateReason)
	defer free()
	var errMsg *C.char
	if !bool(C.nx_biometric_evaluate(reason, &errMsg)) {
		if msg := cStr(errMsg); msg != "" {
			return "", errors.New(msg)
		}
		return "", errors.New("面容验证未通过")
	}
	value, found, err := keychainRead()
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrNoMaterial
	}
	return value, nil
}

// Enable 把解锁材料(凭据库主密码)写入 Keychain, 仅设备级保护(WhenUnlockedThisDeviceOnly)。
func Enable(password string) error {
	if password == "" {
		return errors.New("解锁材料为空")
	}
	account, freeAccount := cString(materialAccount)
	defer freeAccount()
	value, freeValue := cString(password)
	defer freeValue()
	var errMsg *C.char
	if !bool(C.nx_keychain_set(account, value, &errMsg)) {
		if msg := cStr(errMsg); msg != "" {
			return errors.New(msg)
		}
		return errors.New("钥匙串写入失败")
	}
	return nil
}

// Disable 删除 Keychain 中的解锁材料; 不存在视为成功。
func Disable() error {
	account, free := cString(materialAccount)
	defer free()
	var errMsg *C.char
	if !bool(C.nx_keychain_delete(account, &errMsg)) {
		if msg := cStr(errMsg); msg != "" {
			return errors.New(msg)
		}
		return errors.New("钥匙串删除失败")
	}
	return nil
}

func keychainHasMaterial() (bool, error) {
	account, free := cString(materialAccount)
	defer free()
	var errMsg *C.char
	has := bool(C.nx_keychain_has(account, &errMsg))
	if msg := cStr(errMsg); msg != "" {
		return false, errors.New(msg)
	}
	return has, nil
}

func keychainRead() (string, bool, error) {
	account, free := cString(materialAccount)
	defer free()
	var value *C.char
	var errMsg *C.char
	found := bool(C.nx_keychain_get(account, &value, &errMsg))
	if msg := cStr(errMsg); msg != "" {
		return "", false, errors.New(msg)
	}
	if !found {
		return "", false, nil
	}
	return cStr(value), true, nil
}
