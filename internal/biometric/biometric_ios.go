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

// Current summarizes device biometric capability and Keychain material state.
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

// Unlock returns the Keychain unlock material after triggering the system face/fingerprint verification.
// ACL-protected items get their verification prompt from SecItemCopyMatching itself (single path);
// legacy items not yet migrated keep the old flow of explicit LAContext verification before reading the value.
// User cancellation or verification failure returns an error (message is display-ready); the material never leaks.
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

// Enable rebuilds the unlock material (credential vault master password) as an ACL item bound to the current face set
// (WhenUnlockedThisDeviceOnly + BiometryCurrentSet); the old material becomes invalid after faces are added or removed.
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

// Disable deletes the unlock material from the Keychain; a missing item counts as success.
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
		return errors.New("生物识别验证未通过")
	}
	return nil
}

// migrateLegacyItem silently rebuilds a legacy item without ACL into an ACL item: direct reads while the device is unlocked
// show no prompt; the ACL is prepared before deleting the old and creating the new, a failed creation leaves the original item
// untouched, and a failed rebuild can be recovered by re-enabling, with Unlock's explicit verification path as the fallback.
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
