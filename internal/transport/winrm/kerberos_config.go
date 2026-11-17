package winrm

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

const defaultKDCPort = "88"

// KerberosConfig holds the optional Kerberos settings. Every field may stay empty: the realm then comes from
// user@REALM, DOMAIN\user or Config.Domain, the KDCs from the realm's DNS SRV records and the SPN from the host.
type KerberosConfig struct {
	Realm string
	KDCs  []string
	SPN   string

	// username is the client principal name without its realm, filled in by normalization.
	username string
}

func (c Config) normalizedKerberos() (KerberosConfig, error) {
	k := c.Kerberos
	username, realm := splitKerberosUser(c.User)
	if username == "" {
		return KerberosConfig{}, errors.New("Kerberos 用户名为空")
	}
	if realm == "" {
		realm = c.Domain
	}
	if explicit := strings.TrimSpace(k.Realm); explicit != "" {
		realm = explicit
	}
	realm = strings.ToUpper(strings.TrimSpace(realm))
	if realm == "" {
		return KerberosConfig{}, errors.New("Kerberos 需要域（realm）：请以 user@域名 的形式填写用户名，或在 Kerberos 设置中填写域")
	}
	if strings.ContainsAny(realm, " \t/@\\") {
		return KerberosConfig{}, fmt.Errorf("Kerberos 域 %q 无效：应为域的 DNS 名称，如 CORP.EXAMPLE.COM", realm)
	}
	spn, err := kerberosSPN(c.Host, k.SPN)
	if err != nil {
		return KerberosConfig{}, err
	}
	kdcs := make([]string, 0, len(k.KDCs))
	for _, raw := range k.KDCs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		kdc, err := normalizeKDCAddress(raw)
		if err != nil {
			return KerberosConfig{}, err
		}
		kdcs = append(kdcs, kdc)
	}
	if c.Password == "" {
		return KerberosConfig{}, errors.New("Kerberos 认证需要密码凭据：请为资产选择或填写密码")
	}
	return KerberosConfig{Realm: realm, KDCs: kdcs, SPN: spn, username: username}, nil
}

// splitKerberosUser separates user@REALM and DOMAIN\user into the user name and the realm candidate.
func splitKerberosUser(user string) (username, realm string) {
	user = strings.TrimSpace(user)
	if domain, name, ok := strings.Cut(user, `\`); ok {
		return strings.TrimSpace(name), strings.TrimSpace(domain)
	}
	if at := strings.LastIndex(user, "@"); at >= 0 {
		return strings.TrimSpace(user[:at]), strings.TrimSpace(user[at+1:])
	}
	return user, ""
}

// kerberosSPN returns the service principal WinRM registers by default (HTTP/<host>) unless an override is given.
// An IP address has no default SPN, so it needs the override.
func kerberosSPN(host, override string) (string, error) {
	override = strings.TrimSpace(override)
	if override != "" {
		service, instance, ok := strings.Cut(override, "/")
		if !ok || service == "" || instance == "" || strings.ContainsAny(override, " \t@") {
			return "", fmt.Errorf("Kerberos SPN %q 无效：应为 服务/主机 的形式，如 HTTP/server.corp.example.com，且不含 @域", override)
		}
		return override, nil
	}
	host = strings.TrimSuffix(strings.Trim(strings.TrimSpace(host), "[]"), ".")
	if _, err := netip.ParseAddr(host); err == nil {
		return "", errors.New("Kerberos 需要主机名：IP 地址没有默认的 SPN，请改用主机的完整域名，或在 Kerberos 设置中指定 SPN")
	}
	if host == "" {
		return "", errors.New("Kerberos 需要主机名")
	}
	return "HTTP/" + strings.ToLower(host), nil
}

func normalizeKDCAddress(raw string) (string, error) {
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		// No port, or a bare IPv6 literal.
		host, port = strings.Trim(raw, "[]"), defaultKDCPort
	}
	if host == "" || strings.ContainsAny(host, " \t/@") {
		return "", fmt.Errorf("KDC 地址 %q 无效：应为 主机 或 主机:端口", raw)
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", fmt.Errorf("KDC 地址 %q 的端口无效", raw)
	}
	return net.JoinHostPort(host, port), nil
}
