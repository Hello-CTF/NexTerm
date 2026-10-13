// Package biometric 提供 iOS/Android 生物识别评估与安全存储,
// 用于面容/指纹快捷解锁: 解锁材料为凭据库主密码, 验证成功后走既有 vault_unlock 路径。
package biometric

import "errors"

// ErrUnavailable 表示当前平台不支持生物识别解锁(非 iOS/Android 或设备无面容/指纹)。
var ErrUnavailable = errors.New("当前设备不支持生物识别解锁")

// ErrNoMaterial 表示安全存储中尚未保存解锁材料。
var ErrNoMaterial = errors.New("尚未保存生物识别解锁材料")

// Status 汇总生物识别能力与解锁材料状态, 供前端决定是否展示入口。
type Status struct {
	Available   bool `json:"available"`
	CanEvaluate bool `json:"canEvaluate"`
	HasMaterial bool `json:"hasMaterial"`
}

// materialAccount 是解锁材料在安全存储中的固定 account, service 为应用标识。
const materialAccount = "biometric-unlock"

// evaluateReason 是系统面容验证弹窗中展示的用途说明。
const evaluateReason = "解锁 NexTerm 数据密钥"
