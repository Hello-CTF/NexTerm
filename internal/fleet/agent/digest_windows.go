//go:build windows

package agent

import "golang.org/x/sys/windows"

// currentIdentity 返回与 internal/supervisor 一致的 windows 用户标识
// (当前进程令牌的用户 SID 串), 用于计算 hello 上报的 supervisor 状态摘要。
func currentIdentity() (string, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}
