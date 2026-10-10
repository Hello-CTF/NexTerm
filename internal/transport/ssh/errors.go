package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	gossh "golang.org/x/crypto/ssh"
)

type ErrorKind string

const (
	ErrorKindConfig         ErrorKind = "config"
	ErrorKindDNS            ErrorKind = "dns"
	ErrorKindRefused        ErrorKind = "refused"
	ErrorKindUnreachable    ErrorKind = "unreachable"
	ErrorKindTimeout        ErrorKind = "timeout"
	ErrorKindAuth           ErrorKind = "auth"
	ErrorKindHostKeyPending ErrorKind = "host_key_pending"
	ErrorKindHostKeyChanged ErrorKind = "host_key_changed"
	ErrorKindProxy          ErrorKind = "proxy"
	ErrorKindHandshake      ErrorKind = "handshake"
	ErrorKindNetwork        ErrorKind = "network"
	ErrorKindCanceled       ErrorKind = "canceled"
)

type ConnectError struct {
	Kind  ErrorKind
	Op    string
	Host  string
	Port  int
	Proxy string
	Jump  string
	Hint  string
	Err   error
}

func (e *ConnectError) Error() string {
	var message strings.Builder
	message.WriteString("SSH ")
	message.WriteString(e.Op)
	if e.Host != "" {
		message.WriteString(" ")
		message.WriteString(endpointString(e.Host, e.Port))
	}
	if e.Proxy != "" {
		message.WriteString(" via ")
		message.WriteString(e.Proxy)
	}
	if e.Jump != "" {
		message.WriteString(" through jump host ")
		message.WriteString(e.Jump)
	}
	message.WriteString(": ")
	message.WriteString(e.Err.Error())
	if e.Hint != "" {
		message.WriteString(". ")
		message.WriteString(e.Hint)
	}
	return message.String()
}

func (e *ConnectError) Unwrap() error {
	return e.Err
}

func (e *ConnectError) UserMessage() string {
	var keyErr *HostKeyError
	if errors.As(e, &keyErr) {
		return keyErr.UserMessage()
	}
	target := "目标主机"
	if e.Host != "" {
		target = endpointString(e.Host, e.Port)
	}
	if e.Kind == ErrorKindCanceled {
		return "SSH 连接 " + target + " 已取消"
	}
	var message strings.Builder
	message.WriteString("SSH 连接 ")
	message.WriteString(target)
	if e.Jump != "" {
		message.WriteString("（经由跳板机 ")
		message.WriteString(e.Jump)
		message.WriteString("）")
	}
	if e.Proxy != "" {
		message.WriteString("（经由代理 ")
		message.WriteString(e.Proxy)
		message.WriteString("）")
	}
	message.WriteString("失败：")
	message.WriteString(userGuidanceForKind(e.Kind))
	return message.String()
}

func userGuidanceForKind(kind ErrorKind) string {
	switch kind {
	case ErrorKindConfig:
		return "配置无效，请检查主机、端口、用户名与认证方式设置"
	case ErrorKindDNS:
		return "无法解析主机名，请检查主机地址是否拼写正确以及 DNS 是否可用"
	case ErrorKindRefused:
		return "目标主机拒绝连接，请确认主机在线且 SSH 端口已开放"
	case ErrorKindUnreachable:
		return "目标主机不可达，请检查网络路由与防火墙设置"
	case ErrorKindTimeout:
		return "连接超时，请检查网络连通性与防火墙设置"
	case ErrorKindAuth:
		return "认证失败，请检查用户名、密码、私钥或 SSH agent 配置"
	case ErrorKindHostKeyPending:
		return "请核对主机指纹后确认是否信任该主机"
	case ErrorKindHostKeyChanged:
		return "主机指纹与已记录的不一致，请与服务器管理员核对后再确认替换"
	case ErrorKindProxy:
		return "代理连接失败，请检查代理地址、凭据以及代理是否允许连接目标"
	case ErrorKindHandshake:
		return "SSH 握手失败，请检查服务器 SSH 服务状态与其日志"
	case ErrorKindNetwork:
		return "网络连接失败，请检查与目标主机的网络连通性"
	}
	return "连接失败，请检查网络与资产配置"
}

func (e *ConnectError) withJump(jump string) *ConnectError {
	e.Jump = jump
	return e
}

func newConnectError(kind ErrorKind, op, host string, port int, proxy string, err error) *ConnectError {
	return &ConnectError{Kind: kind, Op: op, Host: host, Port: port, Proxy: proxy, Hint: hintForKind(kind), Err: err}
}

func hintForKind(kind ErrorKind) string {
	switch kind {
	case ErrorKindConfig:
		return "Check the asset host, port, user, and authentication settings"
	case ErrorKindDNS:
		return "Check the hostname for typos and make sure DNS resolution works"
	case ErrorKindRefused:
		return "Check that the host is online and the SSH port is open"
	case ErrorKindUnreachable:
		return "Check network routes and firewall rules to the host"
	case ErrorKindTimeout:
		return "The host did not respond in time; check network connectivity and firewall rules"
	case ErrorKindAuth:
		return "Check the username, password, private key, or SSH agent configuration"
	case ErrorKindHostKeyPending:
		return "Verify the fingerprint with the server administrator, then accept the host key"
	case ErrorKindHostKeyChanged:
		return "The host key differs from the recorded one; verify the new fingerprint with the server administrator before accepting it"
	case ErrorKindProxy:
		return "Check the proxy URL, its credentials, and that the proxy allows connections to the target"
	case ErrorKindHandshake:
		return "The SSH handshake failed; check the server SSH service and its logs"
	case ErrorKindNetwork:
		return "Check network connectivity to the host"
	case ErrorKindCanceled:
		return "The connection attempt was canceled"
	}
	return ""
}

func classifyErrorKind(err error) (ErrorKind, bool) {
	if err == nil {
		return "", false
	}
	if errors.Is(err, context.Canceled) {
		return ErrorKindCanceled, true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorKindTimeout, true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsTimeout {
			return ErrorKindTimeout, true
		}
		return ErrorKindDNS, true
	}
	if kind, ok := classifySyscallKind(err); ok {
		return kind, true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrorKindTimeout, true
	}
	return "", false
}

func classifyDialError(host string, port int, err error) *ConnectError {
	op := "dial"
	proxy := ""
	var proxyErr *proxyError
	proxied := errors.As(err, &proxyErr)
	if proxied {
		op = "proxy connect"
		proxy = proxyErr.endpoint
	}
	kind, classified := classifyErrorKind(err)
	if !classified {
		if proxied {
			kind = ErrorKindProxy
		} else {
			kind = ErrorKindNetwork
		}
	}
	connectErr := newConnectError(kind, op, host, port, proxy, err)
	if proxied && (kind == ErrorKindDNS || kind == ErrorKindRefused || kind == ErrorKindUnreachable || kind == ErrorKindTimeout || kind == ErrorKindNetwork) {
		connectErr.Hint = "Check that the proxy is reachable and allows connections to the target"
	}
	return connectErr
}

func classifyHandshakeError(host string, port int, proxy, jump string, err error) *ConnectError {
	var keyErr *HostKeyError
	if errors.As(err, &keyErr) {
		kind := ErrorKindHostKeyChanged
		if keyErr.Pending {
			kind = ErrorKindHostKeyPending
		}
		return newConnectError(kind, "host key verification", host, port, proxy, keyErr).withJump(jump)
	}
	if strings.Contains(err.Error(), "unable to authenticate") {
		return newConnectError(ErrorKindAuth, "authentication", host, port, proxy, err).withJump(jump)
	}
	kind, classified := classifyErrorKind(err)
	if !classified {
		kind = ErrorKindHandshake
	}
	return newConnectError(kind, "handshake", host, port, proxy, err).withJump(jump)
}

func classifyJumpDialError(host string, port int, jump string, err error) *ConnectError {
	kind, classified := classifyErrorKind(err)
	if !classified {
		kind = ErrorKindNetwork
		var openErr *gossh.OpenChannelError
		if errors.As(err, &openErr) {
			kind = classifyJumpDialMessage(openErr.Message)
		}
	}
	connectErr := newConnectError(kind, "jump dial", host, port, "", err).withJump(jump)
	switch kind {
	case ErrorKindRefused:
		connectErr.Hint = "The target refused the connection from the jump host; check that the target SSH port is open"
	case ErrorKindTimeout:
		connectErr.Hint = "The jump host could not reach the target in time; check connectivity between them"
	case ErrorKindDNS:
		connectErr.Hint = "The jump host could not resolve the target name; check the target hostname"
	case ErrorKindCanceled:
	default:
		connectErr.Hint = "Check that the target is reachable from the jump host and its SSH port is open"
	}
	return connectErr
}

func classifyJumpDialMessage(message string) ErrorKind {
	lowered := strings.ToLower(message)
	switch {
	case strings.Contains(lowered, "refused"):
		return ErrorKindRefused
	case strings.Contains(lowered, "timed out"), strings.Contains(lowered, "timeout"):
		return ErrorKindTimeout
	case strings.Contains(lowered, "no route"):
		return ErrorKindUnreachable
	case strings.Contains(lowered, "name or service not known"),
		strings.Contains(lowered, "temporary failure in name resolution"),
		strings.Contains(lowered, "nodename nor servname"):
		return ErrorKindDNS
	}
	return ErrorKindNetwork
}

func dialHopLabels(cfg Config, viaJump bool) (proxy, jump string) {
	if viaJump {
		return "", endpointString(cfg.Jump.Host, cfg.Jump.Port)
	}
	if cfg.ProxyCommand != "" {
		return "proxy-command", ""
	}
	return sanitizedProxyEndpoint(cfg.ProxyURL), ""
}

func contextErrorKind(err error) ErrorKind {
	if errors.Is(err, context.Canceled) {
		return ErrorKindCanceled
	}
	return ErrorKindTimeout
}

type hopConfigError struct {
	host string
	port int
	err  error
}

func (e *hopConfigError) Error() string {
	return e.err.Error()
}

func (e *hopConfigError) Unwrap() error {
	return e.err
}

func wrapJumpConfigError(jump *Config, err error) error {
	var hopErr *hopConfigError
	if errors.As(err, &hopErr) {
		return err
	}
	host, port := jump.Host, jump.Port
	if port == 0 {
		port = 22
	}
	return &hopConfigError{host: host, port: port, err: err}
}

func configErrorEndpoint(cfg Config, err error) (string, int) {
	var hopErr *hopConfigError
	if errors.As(err, &hopErr) {
		return hopErr.host, hopErr.port
	}
	return cfg.Host, cfg.Port
}

type proxyError struct {
	endpoint string
	err      error
}

func (e *proxyError) Error() string {
	if e.endpoint == "" {
		return fmt.Sprintf("SSH proxy: %v", e.err)
	}
	return fmt.Sprintf("SSH proxy %s: %v", e.endpoint, e.err)
}

func (e *proxyError) Unwrap() error {
	return e.err
}

func proxyEndpoint(u *url.URL) string {
	if u == nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func sanitizedProxyEndpoint(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return proxyEndpoint(u)
}
