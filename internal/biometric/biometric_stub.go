//go:build !ios && !android

package biometric

// 非 iOS/Android 平台占位实现: 能力不可用, 调用一律返回 ErrUnavailable,
// 保证全仓 go build / go vet / go test 在 mac、linux、windows 通过。

func Current() (Status, error) {
	return Status{Available: false}, nil
}

func Unlock() (string, error) {
	return "", ErrUnavailable
}

func Enable(string) error {
	return ErrUnavailable
}

func Disable() error {
	return ErrUnavailable
}
