//go:build !darwin

package netproxy

import "golang.org/x/net/http/httpproxy"

// systemConfig reads no system setting outside macOS: the proxy comes from
// the environment only.
func systemConfig() httpproxy.Config { return httpproxy.Config{} }
