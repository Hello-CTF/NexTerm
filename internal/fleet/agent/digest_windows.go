//go:build windows

package agent

import "golang.org/x/sys/windows"

// currentIdentity returns the windows user identifier consistent with internal/supervisor
// (the user SID string of the current process token), used to compute the supervisor state digest reported in hello.
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
