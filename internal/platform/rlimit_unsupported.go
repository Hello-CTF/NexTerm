//go:build !unix

package platform

const DefaultNoFileLimit = 1048576

func RaiseNoFileLimit(uint64) (uint64, error) {
	return 0, nil
}
