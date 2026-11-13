//go:build darwin

package netproxy

import (
	"context"
	"os/exec"
	"time"

	"golang.org/x/net/http/httpproxy"
)

func systemConfig() httpproxy.Config {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return httpproxy.Config{}
	}
	return parseScutil(string(output))
}
