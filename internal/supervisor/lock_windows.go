//go:build windows

package supervisor

func lockSocket(_ string) (func(), error) {
	return func() {}, nil
}
