//go:build darwin

package agent

import (
	"os"
	"strconv"
)

func launchdUID() string {
	return strconv.Itoa(os.Getuid())
}
