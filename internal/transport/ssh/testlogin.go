package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	gossh "golang.org/x/crypto/ssh"
)

// LoginFailureClass is the coarse failure class of a test login, grouped for
// result presentation: network, host key, authentication, vault, or config.
type LoginFailureClass string

const (
	LoginFailureNetwork LoginFailureClass = "network"
	LoginFailureHostKey LoginFailureClass = "host_key"
	LoginFailureAuth    LoginFailureClass = "auth"
	LoginFailureVault   LoginFailureClass = "vault"
	LoginFailureConfig  LoginFailureClass = "config"
)

// VaultError marks an SSH credential failure that happened before the
// handshake, while materializing the credential from the vault. TestLogin
// never returns it; callers wrap vault resolution failures so
// ClassifyLoginFailure reports them as LoginFailureVault.
type VaultError struct{ Err error }

func (e *VaultError) Error() string { return "SSH credential vault: " + e.Err.Error() }
func (e *VaultError) Unwrap() error { return e.Err }

// ClassifyLoginFailure maps a test-login error to its coarse failure class.
// It returns an empty class when err is nil.
func ClassifyLoginFailure(err error) LoginFailureClass {
	if err == nil {
		return ""
	}
	var vaultErr *VaultError
	if errors.As(err, &vaultErr) {
		return LoginFailureVault
	}
	var keyErr *HostKeyError
	if errors.As(err, &keyErr) {
		return LoginFailureHostKey
	}
	var connectErr *ConnectError
	if errors.As(err, &connectErr) {
		switch connectErr.Kind {
		case ErrorKindConfig:
			return LoginFailureConfig
		case ErrorKindHostKeyPending, ErrorKindHostKeyChanged:
			return LoginFailureHostKey
		case ErrorKindAuth:
			return LoginFailureAuth
		}
	}
	return LoginFailureNetwork
}

// TestLogin runs a real SSH handshake with the configured host key and
// credential checks, then closes the connection. It never registers a managed
// session, never opens channels, and never persists host keys: the host key
// check is strictly read-only against recorded and approved keys, so unknown
// keys are never accepted regardless of Config.AutoAcceptUnknown and
// Config.HostKeyApproval. Config.ConnectTimeout bounds the entire jump chain,
// including hops that rely on the default per-hop timeout.
func TestLogin(ctx context.Context, cfg Config) error {
	validated, err := cfg.validate()
	if err != nil {
		host, port := configErrorEndpoint(cfg, err)
		return newConnectError(ErrorKindConfig, "configuration", host, port, "", err)
	}
	client, err := testLoginHandshake(ctx, validated, 0, countJumps(validated), validated.Progress)
	if err != nil {
		return err
	}
	client.Close()
	return nil
}

// loginConn is the established test-login connection: a raw SSH client,
// possibly chained over a jump hop that closes together with it.
type loginConn interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	Close() error
}

func testLoginHandshake(ctx context.Context, cfg Config, depth, hops int, progress func(ConnectProgress)) (loginConn, error) {
	if depth > maxJumpDepth {
		return nil, newConnectError(ErrorKindConfig, "configuration", cfg.Host, cfg.Port, "", fmt.Errorf("SSH jump chain exceeds %d hops", maxJumpDepth))
	}
	connectCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()

	report := func(stage ConnectStage) {
		if progress == nil {
			return
		}
		hop := 0
		if depth > 0 {
			hop = hops - depth + 1
		}
		progress(ConnectProgress{Stage: stage, Hop: hop, Hops: hops, Host: cfg.Host, Port: cfg.Port})
	}
	report(ConnectStageAuth)
	auth, closers, err := authentication(connectCtx, cfg.Auth)
	if err != nil {
		return nil, newConnectError(ErrorKindAuth, "authentication setup", cfg.Host, cfg.Port, "", err)
	}
	defer func() {
		for _, closer := range closers {
			closer.Close()
		}
	}()

	sshConfig := &gossh.ClientConfig{
		User:          cfg.User,
		Auth:          []gossh.AuthMethod{auth},
		ClientVersion: cfg.ClientVersion,
		Timeout:       cfg.ConnectTimeout,
		HostKeyCallback: func(_ string, _ net.Addr, key gossh.PublicKey) error {
			return checkLoginHostKey(connectCtx, cfg.HostKeys, cfg.Host, cfg.Port, key)
		},
	}

	target := endpointString(cfg.Host, cfg.Port)
	var netConn net.Conn
	var jumpClient loginConn
	if cfg.Jump != nil {
		jumpClient, err = testLoginHandshake(connectCtx, *cfg.Jump, depth+1, hops, progress)
		if err != nil {
			return nil, err
		}
		report(ConnectStageChannel)
		netConn, err = jumpClient.DialContext(connectCtx, "tcp", target)
		if err != nil {
			jumpClient.Close()
			return nil, classifyJumpDialError(cfg.Host, cfg.Port, endpointString(cfg.Jump.Host, cfg.Jump.Port), err)
		}
	} else {
		report(ConnectStageDial)
		netConn, err = dialSSH(connectCtx, cfg, cfg.Host, cfg.Port)
		if err != nil {
			return nil, classifyDialError(cfg.Host, cfg.Port, err)
		}
	}
	stop := context.AfterFunc(connectCtx, func() { netConn.Close() })
	report(ConnectStageHandshake)
	conn, channels, requests, err := gossh.NewClientConn(netConn, target, sshConfig)
	stopped := stop()
	if err == nil && !stopped && connectCtx.Err() != nil {
		conn.Close()
		err = connectCtx.Err()
	}
	if err != nil {
		netConn.Close()
		if jumpClient != nil {
			jumpClient.Close()
		}
		proxy, jump := dialHopLabels(cfg, jumpClient != nil)
		if connectCtx.Err() != nil {
			return nil, newConnectError(contextErrorKind(connectCtx.Err()), "handshake", cfg.Host, cfg.Port, proxy, connectCtx.Err()).withJump(jump)
		}
		return nil, classifyHandshakeError(cfg.Host, cfg.Port, proxy, jump, err)
	}
	client := gossh.NewClient(conn, channels, requests)
	if jumpClient != nil {
		return &loginJumpClient{Client: client, jump: jumpClient}, nil
	}
	return client, nil
}

// loginJumpClient closes the jump hop together with the target connection.
type loginJumpClient struct {
	*gossh.Client
	jump loginConn
}

func (c *loginJumpClient) Close() error {
	err := c.Client.Close()
	c.jump.Close()
	return err
}

// checkLoginHostKey verifies the presented host key against recorded and
// approved keys without persisting anything. Unknown keys are never accepted.
// A presented key that contradicts a recorded or approved key of the same
// type is reported as changed; the approved ledger counts as recorded trust
// because this read-only check never persists approved keys itself.
func checkLoginHostKey(ctx context.Context, store HostKeyStore, host string, port int, presented gossh.PublicKey) error {
	key := newHostKey(host, port, presented)
	known, err := store.HostKeys(ctx, host, port)
	if err != nil {
		return fmt.Errorf("load SSH host keys: %w", err)
	}
	var knownSameType []HostKey
	recorded := make(map[string]struct{})
	for _, candidate := range known {
		if candidate.KeyType != key.KeyType {
			continue
		}
		knownSameType = append(knownSameType, candidate)
		recorded[candidate.Fingerprint] = struct{}{}
		if candidate.Key == key.Key {
			return nil
		}
	}
	approved, err := loadApprovedHostKeys(ctx, store, host, port)
	if err != nil {
		return fmt.Errorf("load approved SSH host keys: %w", err)
	}
	for _, candidate := range approved {
		if candidate.KeyType != key.KeyType {
			continue
		}
		if candidate.Fingerprint == key.Fingerprint {
			return nil
		}
		if _, exists := recorded[candidate.Fingerprint]; !exists {
			knownSameType = append(knownSameType, candidate)
			recorded[candidate.Fingerprint] = struct{}{}
		}
	}
	if len(knownSameType) == 0 {
		return &HostKeyError{Pending: true, Presented: key}
	}
	return &HostKeyError{Presented: key, Known: knownSameType}
}

// DescribeLoginFailure turns a test-login or reachability error into a stable
// result kind plus a plain-language message that names the next step. It never
// echoes the underlying error text, so credential material and file contents
// cannot reach the UI. portOnly selects wording for TCP-only probes, where no
// SSH protocol is involved. Kinds are the ConnectError kinds, plus
// "credential" for local credential problems (unreadable private key, wrong
// passphrase, missing agent) and "internal" for unclassified errors.
func DescribeLoginFailure(err error, portOnly bool) (kind, message string) {
	if err == nil {
		return "", ""
	}
	var connectErr *ConnectError
	if !errors.As(err, &connectErr) {
		return "internal", "测试没有完成，请重试；如果反复出现，请查看应用日志"
	}
	target := "目标主机"
	if connectErr.Host != "" {
		target = endpointString(connectErr.Host, connectErr.Port)
	}
	if connectErr.Jump != "" {
		target += "（经由跳板机 " + connectErr.Jump + "）"
	}
	var keyErr *HostKeyError
	errors.As(connectErr, &keyErr)
	switch connectErr.Kind {
	case ErrorKindAuth:
		if connectErr.Op == "authentication setup" {
			return "credential", target + " 的登录凭据不可用：" + credentialProblem(connectErr.Err)
		}
		return "auth", target + " 已连通，但账号或凭据被拒绝。请检查用户名、密码或私钥是否正确，以及该账号是否允许登录"
	case ErrorKindHostKeyPending:
		fingerprint := ""
		if keyErr != nil {
			fingerprint = "（指纹 " + keyErr.Presented.Fingerprint + "）"
		}
		return string(connectErr.Kind), target + " 的主机指纹还没有被信任" + fingerprint + "，连接测试不会自动信任它。请先打开该主机的终端，核对指纹并确认后再重新测试"
	case ErrorKindHostKeyChanged:
		fingerprint := ""
		if keyErr != nil {
			fingerprint = "（新指纹 " + keyErr.Presented.Fingerprint + "）"
		}
		return string(connectErr.Kind), target + " 的主机指纹和上次记录的不一样" + fingerprint + "。可能是服务器重装过，也可能被人冒充，请先向管理员核对，确认无误后再在终端里选择替换"
	case ErrorKindCanceled:
		return string(connectErr.Kind), "已取消"
	}
	if portOnly {
		switch connectErr.Kind {
		case ErrorKindConfig:
			return string(connectErr.Kind), "配置无效，请检查主机地址和端口"
		case ErrorKindDNS:
			return string(connectErr.Kind), "无法解析 " + target + " 的主机名，请检查地址是否拼写正确"
		case ErrorKindRefused:
			return string(connectErr.Kind), target + " 拒绝了连接，请确认服务已启动且端口正确"
		case ErrorKindTimeout:
			return string(connectErr.Kind), "连接 " + target + " 超时，请检查网络和防火墙"
		case ErrorKindProxy:
			return string(connectErr.Kind), "通过代理连接 " + target + " 失败，请检查代理设置"
		}
		return string(connectErr.Kind), "无法连到 " + target + "，请检查网络和防火墙"
	}
	return string(connectErr.Kind), connectErr.UserMessage()
}

func credentialProblem(err error) string {
	text := ""
	if err != nil {
		text = err.Error()
	}
	switch {
	case strings.Contains(text, "read SSH private key"):
		return "读取不到私钥文件，请检查路径和文件权限"
	case strings.Contains(text, "parse encrypted SSH private key"):
		return "私钥口令不正确，或私钥格式不受支持"
	case strings.Contains(text, "parse SSH private key"):
		return "私钥无法识别；如果私钥设置了口令，请在凭据里补上口令"
	case strings.Contains(text, "agent"):
		return "SSH agent 不可用，请确认 agent 已启动并加载了密钥"
	}
	return "请检查资产的认证设置"
}
