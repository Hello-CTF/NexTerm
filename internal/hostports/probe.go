package hostports

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const maxURLLength = 2048

// ProbeRequest describes one connectivity test. Method "tcp" uses Host and Port, "http" uses URL.
type ProbeRequest struct {
	Method string `json:"method"`
	Host   string `json:"host"`
	Port   uint16 `json:"port"`
	URL    string `json:"url"`
}

// normalizeHost validates an IP literal or an RFC 1123 host name and returns it without IPv6 brackets.
func normalizeHost(host string) (string, error) {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		inner := host[1 : len(host)-1]
		if ip := net.ParseIP(inner); ip != nil && strings.Contains(inner, ":") {
			return inner, nil
		}
		return "", errors.New("invalid IPv6 address")
	}
	if host == "" {
		return "", errors.New("host is empty")
	}
	if net.ParseIP(host) != nil {
		return host, nil
	}
	if len(host) > 253 {
		return "", errors.New("host name too long")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid host name")
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return "", errors.New("invalid host name")
			}
		}
	}
	return host, nil
}

// urlByteAllowed accepts RFC 3986 characters except those that are significant to shells.
func urlByteAllowed(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("-._~:/?#[]@!&()*+,;=%", c) >= 0
}

func validateURL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > maxURLLength {
		return nil, errors.New("invalid URL length")
	}
	for i := 0; i < len(raw); i++ {
		if !urlByteAllowed(raw[i]) {
			return nil, errors.New("URL contains a disallowed character")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("only http and https URLs are supported")
	}
	if u.User != nil || strings.Contains(u.Host, "@") {
		return nil, errors.New("URLs with credentials are not allowed")
	}
	if _, err := normalizeHost(bracketIfIPv6(u.Hostname())); err != nil {
		return nil, fmt.Errorf("URL host: %w", err)
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
			return nil, errors.New("URL port out of range")
		}
	}
	return u, nil
}

func bracketIfIPv6(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// ValidateProbe rejects anything that is not a plain host and port, or a plain http(s) URL.
func ValidateProbe(req ProbeRequest) error {
	switch req.Method {
	case "tcp":
		if _, err := normalizeHost(req.Host); err != nil {
			return err
		}
		if req.Port == 0 {
			return errors.New("port must be between 1 and 65535")
		}
		return nil
	case "http":
		_, err := validateURL(req.URL)
		return err
	}
	return fmt.Errorf("unsupported probe method %q", req.Method)
}

func quotePOSIX(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func quotePowerShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// ProbeCommand builds the exact command run on the host. Every user value is a separate single-quoted argument.
func ProbeCommand(shell Shell, req ProbeRequest) (string, error) {
	if err := ValidateProbe(req); err != nil {
		return "", err
	}
	host := ""
	if req.Method == "tcp" {
		host, _ = normalizeHost(req.Host)
	}
	switch shell {
	case ShellPOSIX:
		return posixProbe(req, host), nil
	case ShellPowerShell:
		return powerShellProbe(req, host), nil
	}
	script := powerShellProbe(req, host)
	if strings.ContainsAny(script, "\"%^&|<>!") {
		return "", errors.New("this host runs commands through cmd.exe and the probe contains characters cmd cannot pass safely (for example a URL query string); this kind of host is not supported yet")
	}
	return `powershell -NoProfile -NonInteractive -Command "` + script + `"`, nil
}

func posixProbe(req ProbeRequest, host string) string {
	if req.Method == "http" {
		return "curl -sS -o /dev/null -w '%{http_code} %{time_total}\\n' --max-time 10 --proto '=http,https' -- " + quotePOSIX(req.URL)
	}
	h, p := quotePOSIX(host), quotePOSIX(strconv.Itoa(int(req.Port)))
	return "if command -v nc >/dev/null 2>&1; then nc -z -v -w 5 " + h + " " + p +
		"; elif command -v bash >/dev/null 2>&1 && command -v timeout >/dev/null 2>&1; then timeout 5 bash -c 'exec 3<>\"/dev/tcp/$0/$1\"' " + h + " " + p +
		"; else echo 'neither nc nor bash with timeout is available on the host' >&2; exit 127; fi"
}

func powerShellProbe(req ProbeRequest, host string) string {
	if req.Method == "http" {
		return "Invoke-WebRequest -UseBasicParsing -Method Head -TimeoutSec 10 -Uri " + quotePowerShell(req.URL)
	}
	return "try { $c = New-Object System.Net.Sockets.TcpClient; $t = $c.ConnectAsync(" + quotePowerShell(host) + ", " + strconv.Itoa(int(req.Port)) +
		"); if ($t.Wait(5000) -and $c.Connected) { 'open' } else { 'timeout'; exit 1 } } catch { $_.Exception.Message; exit 1 }"
}
