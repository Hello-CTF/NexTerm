package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
	"github.com/Hello-CTF/NexTerm/internal/transport/telnet"
	"github.com/Hello-CTF/NexTerm/internal/transport/winrm"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

type productionConnector struct {
	database *store.Store
	vault    *vault.Vault
	hostKeys *productionHostKeyStore
	events   ipc.Emitter
	device   atomic.Pointer[DeviceConnectorConfig]
	// multiUser is set for account-based servers. Background reconnects can lose the owner in ctx, so the
	// restriction on server-side commands and files must not depend on it alone.
	multiUser bool
}

// restricted reports whether SSH options that run commands or read files on this machine are refused.
func (c *productionConnector) restricted(ctx context.Context) bool {
	return c.multiUser || ipc.OwnerID(ctx) != ""
}

func (o productionAssetOptions) serverSSHFields() store.ServerSSHFields {
	return store.ServerSSHFields{
		ProxyCommand: o.ProxyCommand, AgentSocket: o.AgentSocket, ForwardAgent: o.ForwardAgent,
		AuthKind: o.AuthKind, KeyPath: strings.TrimSpace(o.KeyPath), CertPath: strings.TrimSpace(o.CertPath),
	}
}

// rejectServerSSHOptions refuses restricted SSH settings and names the group when the offending
// authentication setting (authKind agent, keyPath) was inherited from a group default.
func rejectServerSSHOptions(options productionAssetOptions) error {
	fields := options.serverSSHFields()
	err := store.RejectServerSSHFields(fields)
	if err == nil || options.InheritedFrom[store.InheritAuth] == "" {
		return err
	}
	fields.AuthKind, fields.KeyPath = "", ""
	if store.RejectServerSSHFields(fields) != nil {
		return err
	}
	return inheritedError(options, store.InheritAuth, "认证方式", err)
}

type productionAssetOptions struct {
	Host                  string            `json:"host"`
	Port                  int               `json:"port"`
	Username              string            `json:"username"`
	AuthKind              string            `json:"authKind"`
	KeyPath               string            `json:"keyPath"`
	CredID                string            `json:"credId"`
	Password              string            `json:"password"`
	Shell                 string            `json:"shell"`
	CWD                   string            `json:"cwd"`
	Env                   map[string]string `json:"env"`
	Proxy                 string            `json:"proxy"`
	ProxyCommand          string            `json:"proxyCommand"`
	JumpAssetID           string            `json:"jumpAssetId"`
	ForwardAgent          bool              `json:"forwardAgent"`
	CertPath              string            `json:"certPath"`
	ConnectTimeout        int64             `json:"connectTimeout"`
	AutoAcceptUnknownHost bool              `json:"autoAcceptUnknownHost"`
	// StrictHostKey disables auto-accepting unknown host keys on this hop and
	// every jump hop below it, regardless of stored auto-accept options. It is
	// set server-side for unattended cron connects, never relaxed by options.
	StrictHostKey      bool   `json:"strictHostKey"`
	AgentSocket        string `json:"agentSocket"`
	Auth               string `json:"auth"`
	Domain             string `json:"domain"`
	TLS                bool   `json:"tls"`
	AcceptInvalidCerts bool   `json:"acceptInvalidCerts"`
	TLSServerName      string `json:"tlsServerName"`
	CACert             string `json:"caCert"`
	RequestTimeoutMS   int64  `json:"requestTimeoutMs"`
	InitialCommand     string `json:"initialCommand"`
	StartupCommand     string `json:"startupCommand"`
	Encoding           string `json:"encoding"`
	// LocalEcho (auto, on, off) and SendCRLF are Telnet terminal options.
	LocalEcho string `json:"localEcho"`
	SendCRLF  bool   `json:"sendCrlf"`
	DeviceID  string `json:"deviceId"`
	UserID    string `json:"userId"`
	// InheritedFrom maps an inherited field (store.InheritUsername, InheritAuth, InheritJump) to the name of the
	// group that supplied it. It is set only by productionResolvedOptionMap, never read from stored options, so
	// connection errors can name the group that owns the value.
	InheritedFrom map[string]string `json:"inheritedFrom,omitempty"`
}

// annotateInheritedConnectError tells the user which groups supplied the connection settings of a failed SSH
// connect, so a wrong inherited username, credential or jump host can be found. The structured error is kept.
func annotateInheritedConnectError(options productionAssetOptions, err error) error {
	var connectErr *ssh.ConnectError
	if err == nil || len(options.InheritedFrom) == 0 || !errors.As(err, &connectErr) {
		return err
	}
	var parts []string
	for _, field := range []struct{ key, label string }{
		{store.InheritUsername, "用户名"}, {store.InheritAuth, "认证方式"}, {store.InheritJump, "跳板机"},
	} {
		if group := options.InheritedFrom[field.key]; group != "" {
			parts = append(parts, fmt.Sprintf("%s继承自分组「%s」", field.label, group))
		}
	}
	note := "该资产的" + strings.Join(parts, "，") + "，请检查分组的连接默认值"
	if connectErr.Hint != "" {
		connectErr.Hint += "。" + note
	} else {
		connectErr.Hint = note
	}
	return err
}

// inheritedError names the group an inherited value came from, keeping the error code of the cause.
func inheritedError(options productionAssetOptions, field, what string, err error) error {
	group := options.InheritedFrom[field]
	if err == nil || group == "" {
		return err
	}
	message := fmt.Sprintf("%s继承自分组「%s」，请检查该分组的连接默认值: %s", what, group, err.Error())
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return ipc.WrapError(appErr.Code, message, err)
	}
	return ipc.WrapError(ipc.CodeBadParam, message, err)
}

func newProductionConnector(ctx context.Context, database *store.Store, credentialVault *vault.Vault, dataDir string, events ipc.Emitter, shared bool) (*productionConnector, error) {
	hostKeys, err := newProductionHostKeyStore(ctx, filepath.Join(dataDir, "known_hosts.json"), database, shared)
	if err != nil {
		return nil, err
	}
	return &productionConnector{database: database, vault: credentialVault, hostKeys: hostKeys, events: events, multiUser: !shared}, nil
}

func (c *productionConnector) Connect(ctx context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
	var options productionAssetOptions
	if asset.Options != nil {
		raw, err := json.Marshal(asset.Options)
		if err != nil {
			return nil, ipc.BadParam(err)
		}
		if err := json.Unmarshal(raw, &options); err != nil {
			return nil, ipc.BadParam(fmt.Errorf("会话资产选项无效: %w", err))
		}
	}
	if ipc.OwnerID(ctx) == "" && options.UserID != "" && asset.StoreBacked && (asset.Kind == session.KindSSH || asset.Kind == session.KindDocker) {
		// Session connectors can lose request values; only an owner bound by the authorized asset conversion may be restored.
		ownerID, err := c.database.AssetOwnerID(ctx, asset.ID)
		if err != nil {
			return nil, err
		}
		if ownerID != options.UserID {
			return nil, ipc.NewError(ipc.CodeForbidden, "会话资产所有者无效")
		}
		ctx = ipc.WithUserID(ctx, ownerID)
	}
	var transport base.Transport
	var err error
	if options.Encoding != "" {
		if _, err := terminal.ParseEncoding(options.Encoding); err != nil {
			return nil, ipc.BadParam(fmt.Errorf("资产终端编码无效: %w", err))
		}
	}
	if strings.TrimSpace(options.StartupCommand) != "" && asset.Kind != session.KindSSH && asset.Kind != session.KindDocker {
		return nil, ipc.BadParam(fmt.Errorf("仅 SSH 资产支持启动命令，当前资产类型为 %s", asset.Kind))
	}
	switch asset.Kind {
	case session.KindLocal:
		if c.restricted(ctx) {
			return nil, ipc.NewError(ipc.CodeUnsupported, "多用户服务端不支持本地终端资产（会在服务器上启动 shell），请改用 SSH 资产或设备")
		}
		transport = local.NewWithConfig(local.Config{Shell: options.Shell, CWD: options.CWD, Env: options.Env})
	case session.KindSSH, session.KindDocker:
		transport, err = c.connectSSH(ctx, asset.ID, options, generation)
		err = annotateInheritedConnectError(options, err)
	case session.KindWinRM:
		transport, err = c.connectWinRM(ctx, options)
	case session.KindDevice:
		transport, err = c.connectDevice(ctx, options, generation)
	case session.KindTelnet:
		transport, err = connectTelnet(ctx, options)
	case session.KindRDP, session.KindVNC:
		err = ipc.NewError(ipc.CodeUnsupported, "RDP/VNC 资产不支持终端连接，请使用远程桌面打开")
	default:
		err = fmt.Errorf("%w: %s transport", session.ErrUnsupported, asset.Kind)
	}
	if err != nil {
		return nil, err
	}
	if options.InitialCommand != "" {
		execCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		result, execErr := transport.Exec(execCtx, options.InitialCommand, base.ExecOptions{})
		cancel()
		if generation == 1 {
			c.reportInitialCommandFailure(ctx, options.InitialCommand, result, execErr)
		}
		if err := ctx.Err(); err != nil {
			_ = transport.Close()
			return nil, err
		}
	}
	return transport, nil
}

func (c *productionConnector) reportInitialCommandFailure(ctx context.Context, command string, result base.ExecResult, execErr error) {
	if execErr == nil && (result.ExitCode == nil || *result.ExitCode == 0) {
		return
	}
	cause := execErr
	if cause == nil {
		cause = fmt.Errorf("exit code %d: %s", *result.ExitCode, truncatedInitialCommandStderr(result.Stderr))
	}
	reportAppError(ctx, c.events, "session", "初始命令执行失败，未在会话中运行，请检查资产的初始命令配置", fmt.Errorf("command %q: %w", command, cause))
}

func truncatedInitialCommandStderr(stderr string) string {
	trimmed := []rune(strings.TrimSpace(stderr))
	if len(trimmed) > 200 {
		return string(trimmed[:200]) + "..."
	}
	return string(trimmed)
}

const maxJumpAssetHops = 8

func (c *productionConnector) connectSSH(ctx context.Context, assetID string, options productionAssetOptions, generation uint64) (base.Transport, error) {
	chain := []string{}
	if assetID != "" {
		chain = append(chain, assetID)
	}
	config, err := c.sshConfig(ctx, options, generation, chain)
	if err != nil {
		return nil, err
	}
	if assetID != "" {
		config.Progress = func(progress ssh.ConnectProgress) {
			_ = ipc.Emit(ctx, c.events, ipc.TopicConnectProgress, ipc.ConnectProgressEvent{
				AssetID: assetID,
				Stage:   string(progress.Stage),
				Hop:     progress.Hop,
				Hops:    progress.Hops,
				Host:    progress.Host,
				Port:    progress.Port,
			})
		}
	}
	return ssh.Connect(ctx, config)
}

func (c *productionConnector) sshConfig(ctx context.Context, options productionAssetOptions, generation uint64, chain []string) (ssh.Config, error) {
	return c.sshConfigHop(ctx, options, generation, chain, false)
}

// sshConfigHop builds the SSH config for one hop; strict disables
// auto-accepting unknown host keys on this hop and every jump hop below it.
func (c *productionConnector) sshConfigHop(ctx context.Context, options productionAssetOptions, generation uint64, chain []string, strict bool) (ssh.Config, error) {
	if c.restricted(ctx) {
		if err := rejectServerSSHOptions(options); err != nil {
			return ssh.Config{}, err
		}
	}
	if options.Host == "" {
		return ssh.Config{}, ipc.BadParam(fmt.Errorf("SSH asset host is required"))
	}
	if options.Port == 0 {
		options.Port = 22
	}
	if options.Username == "" {
		options.Username = "root"
	}
	if (options.ForwardAgent || options.AuthKind == "agent") && options.AgentSocket == "" && os.Getenv("SSH_AUTH_SOCK") == "" {
		return ssh.Config{}, ipc.NewError(ipc.CodeBadParam, "已启用 SSH Agent，但未配置其 socket，且环境变量 SSH_AUTH_SOCK 为空")
	}
	strict = strict || options.StrictHostKey
	config := ssh.Config{
		Host: options.Host, Port: options.Port, User: options.Username, HostKeys: c.hostKeys,
		ProxyURL: options.Proxy, ProxyCommand: options.ProxyCommand, ForwardAgent: options.ForwardAgent,
		AutoAcceptUnknown: generation == 1 && options.AutoAcceptUnknownHost && !strict,
		ConnectTimeout:    time.Duration(options.ConnectTimeout) * time.Second,
		Env:               options.Env,
	}
	switch options.AuthKind {
	case "password":
		secret, err := c.passwordSecret(ctx, options)
		if err != nil {
			return ssh.Config{}, inheritedError(options, store.InheritAuth, "认证方式", err)
		}
		config.Auth = ssh.AuthConfig{Method: ssh.AuthPassword, Password: secret}
	case "keyboard-interactive":
		secret, _, err := c.secret(ctx, options.CredID)
		if err != nil {
			return ssh.Config{}, inheritedError(options, store.InheritAuth, "认证方式", err)
		}
		config.Auth = ssh.AuthConfig{Method: ssh.AuthKeyboardInteractive, Password: secret}
	case "key":
		auth, err := c.keyAuth(ctx, options)
		if err != nil {
			return ssh.Config{}, inheritedError(options, store.InheritAuth, "认证方式", err)
		}
		config.Auth = auth
	case "agent":
		config.Auth = ssh.AuthConfig{Method: ssh.AuthAgent, AgentSocket: options.AgentSocket}
	default:
		return ssh.Config{}, ipc.BadParam(fmt.Errorf("SSH asset authentication method is required"))
	}
	config.Auth.AgentSocket = options.AgentSocket
	if options.JumpAssetID != "" {
		jump, err := c.jumpConfigHop(ctx, options.JumpAssetID, generation, chain, strict)
		if err != nil {
			return ssh.Config{}, inheritedError(options, store.InheritJump, "跳板机", err)
		}
		config.Jump = &jump
	}
	return config, nil
}

func (c *productionConnector) jumpConfig(ctx context.Context, jumpAssetID string, generation uint64, chain []string) (ssh.Config, error) {
	return c.jumpConfigHop(ctx, jumpAssetID, generation, chain, false)
}

func (c *productionConnector) jumpConfigHop(ctx context.Context, jumpAssetID string, generation uint64, chain []string, strict bool) (ssh.Config, error) {
	for _, visited := range chain {
		if visited == jumpAssetID {
			return ssh.Config{}, ipc.BadParam(fmt.Errorf("SSH jump asset cycle: %s", strings.Join(append(chain, jumpAssetID), " -> ")))
		}
	}
	if len(chain) >= maxJumpAssetHops {
		return ssh.Config{}, ipc.BadParam(fmt.Errorf("SSH jump chain exceeds %d assets", maxJumpAssetHops))
	}
	row, err := c.database.AssetGetResolved(ctx, jumpAssetID)
	if err != nil {
		var appErr *ipc.Error
		if errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound {
			return ssh.Config{}, ipc.BadParam(fmt.Errorf("SSH jump asset %s does not exist", jumpAssetID))
		}
		return ssh.Config{}, fmt.Errorf("load SSH jump asset: %w", err)
	}
	if row.DeletedAt != nil {
		return ssh.Config{}, ipc.BadParam(fmt.Errorf("SSH jump asset %s is deleted", row.Name))
	}
	if row.Kind != session.KindSSH && row.Kind != session.KindDocker {
		return ssh.Config{}, ipc.BadParam(fmt.Errorf("SSH jump asset %s must be an SSH asset", row.Name))
	}
	options, err := productionAssetOptionsFromRow(row)
	if err != nil {
		return ssh.Config{}, err
	}
	return c.sshConfigHop(ctx, options, generation, append(chain, row.ID), strict)
}

// productionResolvedOptionMap is productionAssetOptionMap for a row with group defaults applied. It records which
// group supplied each inherited field; a stored "inheritedFrom" option can never impersonate that.
func productionResolvedOptionMap(resolved store.ResolvedAsset) (map[string]any, error) {
	options, err := productionAssetOptionMap(resolved.AssetRow)
	if err != nil {
		return nil, err
	}
	delete(options, "inheritedFrom")
	inherited := map[string]any{}
	for _, item := range resolved.Inherited {
		if item.Applied {
			inherited[item.Field] = item.GroupName
		}
	}
	if len(inherited) > 0 {
		options["inheritedFrom"] = inherited
	}
	return options, nil
}

func productionAssetOptionMap(row store.AssetRow) (map[string]any, error) {
	options := make(map[string]any)
	if err := json.Unmarshal(store.ParseJSONOr(row.OptionsJSON), &options); err != nil {
		return nil, err
	}
	if row.Host != nil {
		options["host"] = *row.Host
	}
	if row.Port != nil {
		options["port"] = *row.Port
	}
	if row.Username != nil {
		options["username"] = *row.Username
	}
	if row.AuthKind != nil {
		options["authKind"] = *row.AuthKind
	}
	if row.KeyPath != nil {
		options["keyPath"] = *row.KeyPath
	}
	if row.CredID != nil {
		options["credId"] = *row.CredID
	}
	return options, nil
}

func productionAssetOptionsFromRow(row store.ResolvedAsset) (productionAssetOptions, error) {
	var options productionAssetOptions
	merged, err := productionResolvedOptionMap(row)
	if err != nil {
		return options, ipc.BadParam(err)
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return options, ipc.BadParam(err)
	}
	if err := json.Unmarshal(raw, &options); err != nil {
		return options, ipc.BadParam(fmt.Errorf("invalid SSH asset options: %w", err))
	}
	return options, nil
}

func (c *productionConnector) keyAuth(ctx context.Context, options productionAssetOptions) (ssh.AuthConfig, error) {
	auth := ssh.AuthConfig{Method: ssh.AuthKey, KeyPath: options.KeyPath, CertPath: options.CertPath}
	if options.CredID == "" {
		if auth.KeyPath == "" {
			return auth, ipc.BadParam(fmt.Errorf("SSH key path or private-key credential is required"))
		}
		return auth, nil
	}
	secret, row, err := c.secret(ctx, options.CredID)
	if err != nil {
		return auth, err
	}
	if auth.KeyPath != "" {
		if row.Kind != vault.KindPrivateKey {
			auth.Passphrase = secret
		}
		return auth, nil
	}
	if row.Kind != vault.KindPrivateKey {
		return auth, ipc.BadParam(fmt.Errorf("credential %s is not a private key", row.Name))
	}
	payload := vault.ParsePrivateKeyPayload(secret)
	auth.Passphrase = stringValue(payload.Passphrase)
	if payload.File != nil {
		if c.restricted(ctx) {
			return auth, store.RejectServerSSHFile()
		}
		auth.KeyPath = *payload.File
		return auth, nil
	}
	if payload.Key == nil || strings.TrimSpace(*payload.Key) == "" {
		return auth, ipc.BadParam(fmt.Errorf("private-key credential %s has no key content or path", row.Name))
	}
	auth.KeyPEM = []byte(*payload.Key)
	return auth, nil
}

func (c *productionConnector) connectWinRM(ctx context.Context, options productionAssetOptions) (base.Transport, error) {
	if options.Host == "" {
		return nil, ipc.BadParam(fmt.Errorf("WinRM 资产缺少主机地址"))
	}
	if options.Port == 0 {
		options.Port = 5985
	}
	if options.Username == "" {
		options.Username = "Administrator"
	}
	secret, _, err := c.secret(ctx, options.CredID)
	if err != nil {
		return nil, inheritedError(options, store.InheritAuth, "认证方式", err)
	}
	transport, err := winrm.New(winrm.Config{
		Host: options.Host, Port: options.Port, User: options.Username, Password: secret,
		Domain: options.Domain, Auth: winrm.AuthMethod(options.Auth), UseTLS: options.TLS,
		AcceptInvalidCerts: options.AcceptInvalidCerts, TLSServerName: options.TLSServerName,
		CACert: []byte(options.CACert), ProxyURL: options.Proxy,
		RequestTimeout: time.Duration(options.RequestTimeoutMS) * time.Millisecond,
	})
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeWinRM, err.Error(), err)
	}
	return transport, nil
}

func connectTelnet(ctx context.Context, options productionAssetOptions) (base.Transport, error) {
	if strings.TrimSpace(options.Host) == "" {
		return nil, ipc.BadParam(fmt.Errorf("Telnet 资产缺少主机地址"))
	}
	echo, err := telnet.ParseEchoMode(options.LocalEcho)
	if err != nil {
		return nil, ipc.BadParam(fmt.Errorf("Telnet 本地回显设置无效，应为 auto、on 或 off"))
	}
	config := telnet.Config{Host: options.Host, Port: options.Port, LocalEcho: echo, SendCRLF: options.SendCRLF}
	if options.ConnectTimeout > 0 {
		config.DialTimeout = time.Duration(options.ConnectTimeout) * time.Second
	}
	transport, err := telnet.Connect(ctx, config)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		code := ipc.CodeIO
		if errors.Is(err, context.DeadlineExceeded) {
			code = ipc.CodeTimeout
		}
		return nil, ipc.WrapError(code, fmt.Sprintf("无法连接 Telnet 服务器，请检查地址、端口和网络: %v", err), err)
	}
	return transport, nil
}

func (c *productionConnector) secret(ctx context.Context, id string) (string, store.CredentialRow, error) {
	if id == "" {
		return "", store.CredentialRow{}, nil
	}
	row, err := c.database.CredentialGetRow(ctx, id)
	if err != nil {
		return "", store.CredentialRow{}, err
	}
	secret, err := c.vault.DecryptCredentialString(ctx, row)
	return secret, row, err
}

func (c *productionConnector) passwordSecret(ctx context.Context, options productionAssetOptions) (string, error) {
	if options.CredID == "" {
		return options.Password, nil
	}
	secret, _, err := c.secret(ctx, options.CredID)
	return secret, err
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
