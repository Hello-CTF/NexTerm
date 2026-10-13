// Package biometric provides iOS/Android biometric evaluation and secure storage
// for face/fingerprint quick unlock: the unlock material is the credential vault master password, and after successful verification it takes the existing vault_unlock path.
package biometric

import "errors"

// ErrUnavailable means the current platform does not support biometric unlock (not iOS/Android, or the device has no face/fingerprint).
var ErrUnavailable = errors.New("当前设备不支持生物识别解锁")

// ErrNoMaterial means no unlock material has been saved in secure storage yet.
var ErrNoMaterial = errors.New("尚未保存生物识别解锁材料")

// Status summarizes biometric capability and unlock material state, so the frontend can decide whether to show the entry point.
type Status struct {
	Available   bool `json:"available"`
	CanEvaluate bool `json:"canEvaluate"`
	HasMaterial bool `json:"hasMaterial"`
}

// materialAccount is the fixed account of the unlock material in secure storage; service is the app identifier.
const materialAccount = "biometric-unlock"

// evaluateReason is the usage description shown in the system face verification prompt.
const evaluateReason = "解锁 NexTerm 数据密钥"
