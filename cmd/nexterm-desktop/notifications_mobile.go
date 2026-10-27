//go:build android || ios

package main

// Mobile builds have no system notification bridge yet: the Wails service
// only stubs iOS and has no Android implementation.
func newNotificationBackend() (notificationBackend, string) {
	return nil, "移动端暂不支持系统通知"
}
