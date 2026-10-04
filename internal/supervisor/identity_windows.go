//go:build windows

package supervisor

func currentUserIdentity() (string, error) {
	return currentUserSIDString()
}
