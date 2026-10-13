//go:build unix

package agent

import (
	"os"
	"strconv"
)

// currentIdentity returns the unix user identifier consistent with internal/supervisor
// (the euid as a decimal string), used to compute the supervisor state digest reported in hello.
func currentIdentity() (string, error) {
	return strconv.Itoa(os.Geteuid()), nil
}
