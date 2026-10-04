//go:build unix

package supervisor

import (
	"os"
	"strconv"
)

func currentUserIdentity() (string, error) {
	return strconv.Itoa(os.Geteuid()), nil
}
