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

type keychainState int

const (
	keychainEmpty keychainState = iota
	keychainLegacy
	keychainProtected
)

// Current 汇总设备生物识别能力与 Keychain 材料状态。
func Current() (Status, error) {
	status := Status{Available: true, CanEvaluate: bool(C.nx_biometric_can_evaluate())}
	state, err := keychainItemState()
	if err != nil {
		return Status{}, err
	}
	if state == keychainLegacy && migrateLegacyItem() {
		state = keychainProtected
	}
	status.HasMaterial = state != keychainEmpty
	return status, nil
}

// Unlock 触发系统面容/指纹验证后返回 Keychain 中的解锁材料。
// 受 ACL 保护的 item 由 SecItemCopyMatching 自行弹出验证(单一路径);
// 迁移未完成的存量 item 保持先 LAContext 显式验证再读值的旧编排。
// 用户取消或验证失败返回 error(消息可直接展示), 材料不会外泄。
func Unlock() (string, error) {
	state, err := keychainItemState()
	if err != nil {
		return "", err
	}
	if state == keychainEmpty {
		return "", ErrNoMaterial
	}
	if state == keychainProtected {
		value, found, err := keychainRead(evaluateReason)
		if err != nil {
			return "", err
		}
		if !found {
			return "", ErrNoMaterial
		}
		return value, nil
	}
	if !bool(C.nx_biometric_can_evaluate()) {
		return "", ErrUnavailable
	}
	if err := evaluate(); err != nil {
		return "", err
	}
	value, found, err := keychainRead("")
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrNoMaterial
	}
	migrateMaterial(value)
	return value, nil
}

// Enable 把解锁材料(凭据库主密码)重建为绑定当前面容集的 ACL item
// (WhenUnlockedThisDeviceOnly + BiometryCurrentSet), 新增/删除面容后旧材料失效。
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

func evaluate() error {
	reason, free := cString(evaluateReason)
	defer free()
	var errMsg *C.char
	if !bool(C.nx_biometric_evaluate(reason, &errMsg)) {
		if msg := cStr(errMsg); msg != "" {
			return errors.New(msg)
		}
		return errors.New("面容验证未通过")
	}
	return nil
}

// migrateLegacyItem 把存量无 ACL item 静默重建为 ACL item: 设备解锁态下直读
// 不弹窗; ACL 先备妥再删旧建新, 创建失败原 item 不动, 重建失败可重新启用恢复,
// 由 Unlock 的显式验证路径继续兜底。
func migrateLegacyItem() bool {
	value, found, err := keychainRead("")
	if err != nil || !found {
		return false
	}
	return migrateMaterial(value)
}

func migrateMaterial(value string) bool {
	return Enable(value) == nil
}

func keychainItemState() (keychainState, error) {
	account, free := cString(materialAccount)
	defer free()
	var errMsg *C.char
	state := C.nx_keychain_state(account, &errMsg)
	if msg := cStr(errMsg); msg != "" {
		return keychainEmpty, errors.New(msg)
	}
	return keychainState(state), nil
}

func keychainRead(prompt string) (string, bool, error) {
	account, freeAccount := cString(materialAccount)
	defer freeAccount()
	var cPrompt *C.char
	if prompt != "" {
		p, freePrompt := cString(prompt)
		defer freePrompt()
		cPrompt = p
	}
	var value *C.char
	var errMsg *C.char
	found := bool(C.nx_keychain_get(account, cPrompt, &value, &errMsg))
	if msg := cStr(errMsg); msg != "" {
		return "", false, errors.New(msg)
	}
	if !found {
		return "", false, nil
	}
	return cStr(value), true, nil
}
