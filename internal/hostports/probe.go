package hostports

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
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
		return "", errors.New("IPv6 地址无效")
	}
	if host == "" {
		return "", errors.New("主机地址不能为空")
	}
	if net.ParseIP(host) != nil {
		return host, nil
	}
	if len(host) > 253 {
		return "", errors.New("主机名过长")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("主机名无效")
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return "", errors.New("主机名无效")
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
		return nil, errors.New("URL 长度无效")
	}
	for i := 0; i < len(raw); i++ {
		if !urlByteAllowed(raw[i]) {
			return nil, errors.New("URL 含有不允许的字符")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("URL 无效")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("仅支持 http 和 https URL")
	}
	if u.User != nil || strings.Contains(u.Host, "@") {
		return nil, errors.New("URL 不允许包含账号密码")
	}
	if _, err := normalizeHost(bracketIfIPv6(u.Hostname())); err != nil {
		return nil, fmt.Errorf("URL 主机无效: %w", err)
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
			return nil, errors.New("URL 端口超出范围")
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
			return errors.New("端口必须在 1 到 65535 之间")
		}
		return nil
	case "http":
		_, err := validateURL(req.URL)
		return err
	}
	return fmt.Errorf("不支持的测试方式 %q", req.Method)
}

func quotePOSIX(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func quotePowerShell(value string) string {
	return psquote.Quote(value)
}

// ProbeCommand builds the exact command run on the host. Every user value is a separate single-quoted argument.
func ProbeCommand(shell Shell, req ProbeRequest) (string, error) {
	command, _, err := probeCommandAndScript(shell, req)
	return command, err
}

// probeCommandAndScript also returns the PowerShell script hidden inside an -EncodedCommand wrapper, or "" when the
// command is sent as written.
func probeCommandAndScript(shell Shell, req ProbeRequest) (string, string, error) {
	if err := ValidateProbe(req); err != nil {
		return "", "", err
	}
	host := ""
	if req.Method == "tcp" {
		host, _ = normalizeHost(req.Host)
	}
	switch shell {
	case ShellPOSIX:
		return posixProbe(req, host), "", nil
	case ShellPowerShell:
		return powerShellProbe(req, host), "", nil
	}
	// cmd.exe must not see the script: the base64 alphabet contains no character cmd treats specially.
	script := powerShellProbe(req, host)
	return psquote.ShortCommand(script), script, nil
}

func posixProbe(req ProbeRequest, host string) string {
	if req.Method == "http" {
		return "curl -sS -g -o /dev/null -w '%{http_code} %{time_total}\\n' --max-time 10 --proto '=http,https' -- " + quotePOSIX(req.URL)
	}
	h, p := quotePOSIX(host), quotePOSIX(strconv.Itoa(int(req.Port)))
	return "if command -v nc >/dev/null 2>&1; then nc -z -v -w 5 " + h + " " + p +
		"; elif command -v bash >/dev/null 2>&1 && command -v timeout >/dev/null 2>&1; then timeout 5 bash -c 'exec 3<>\"/dev/tcp/$0/$1\"' " + h + " " + p +
		"; else echo '主机上没有 nc，也没有带 timeout 的 bash' >&2; exit 127; fi"
}

func powerShellProbe(req ProbeRequest, host string) string {
	if req.Method == "http" {
		return "Invoke-WebRequest -UseBasicParsing -Method Head -TimeoutSec 10 -Uri " + quotePowerShell(req.URL)
	}
	return "try { $c = New-Object System.Net.Sockets.TcpClient; $t = $c.ConnectAsync(" + quotePowerShell(host) + ", " + strconv.Itoa(int(req.Port)) +
		"); if ($t.Wait(5000) -and $c.Connected) { 'open' } else { 'timeout'; exit 1 } } catch { $_.Exception.Message; exit 1 }"
}
