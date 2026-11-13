package netproxy

import (
	"bufio"
	"net"
	"strings"

	"golang.org/x/net/http/httpproxy"
)

// parseScutil reads the output of macOS `scutil --proxy`. Enabled HTTP and
// HTTPS proxies apply to their schemes; a SOCKS proxy applies to the schemes
// left without one.
func parseScutil(output string) httpproxy.Config {
	values := map[string]string{}
	var exceptions []string
	inExceptions := false
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if inExceptions {
			if line == "}" {
				inExceptions = false
				continue
			}
			if _, value, ok := strings.Cut(line, " : "); ok {
				exceptions = append(exceptions, strings.TrimSpace(value))
			}
			continue
		}
		key, value, ok := strings.Cut(line, " : ")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key == "ExceptionsList" && strings.HasPrefix(value, "<array>") {
			inExceptions = true
			continue
		}
		values[key] = value
	}
	endpoint := func(prefix string) string {
		if values[prefix+"Enable"] != "1" || values[prefix+"Proxy"] == "" {
			return ""
		}
		host := values[prefix+"Proxy"]
		if port := values[prefix+"Port"]; port != "" {
			host = net.JoinHostPort(host, port)
		}
		return host
	}
	config := httpproxy.Config{
		HTTPProxy:  proxyURL("http", endpoint("HTTP")),
		HTTPSProxy: proxyURL("http", endpoint("HTTPS")),
		NoProxy:    noProxyList(exceptions),
	}
	if socks := proxyURL("socks5", endpoint("SOCKS")); socks != "" {
		if config.HTTPProxy == "" {
			config.HTTPProxy = socks
		}
		if config.HTTPSProxy == "" {
			config.HTTPSProxy = socks
		}
	}
	return config
}
