package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/local"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/ssh"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/winrm"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

type productionConnector struct {
	database *store.Store
	vault    *vault.Vault
	hostKeys *productionHostKeyStore
}

type productionAssetOptions struct {
	Host                  string            `json:"host"`
	Port                  int               `json:"port"`
	Username              string            `json:"username"`
	AuthKind              string            `json:"authKind"`
	KeyPath               string            `json:"keyPath"`
	CredID                string            `json:"credId"`
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
	AgentSocket           string            `json:"agentSocket"`
	Auth                  string            `json:"auth"`
	Domain                string            `json:"domain"`
	TLS                   bool              `json:"tls"`
	AcceptInvalidCerts    bool              `json:"acceptInvalidCerts"`
	TLSServerName         string            `json:"tlsServerName"`
	CACert                string            `json:"caCert"`
	RequestTimeoutMS      int64             `json:"requestTimeoutMs"`
	InitialCommand        string            `json:"initialCommand"`
}

func newProductionConnector(database *store.Store, credentialVault *vault.Vault, dataDir string) (*productionConnector, error) {
	hostKeys, err := newProductionHostKeyStore(filepath.Join(dataDir, "known_hosts.json"), database)
	if err != nil {
		return nil, err
	}
	return &productionConnector{database: database, vault: credentialVault, hostKeys: hostKeys}, nil
}

func (c *productionConnector) Connect(ctx context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
	var options productionAssetOptions
	if asset.Options != nil {
		raw, err := json.Marshal(asset.Options)
		if err != nil {
			return nil, ipc.BadParam(err)
		}
		if err := json.Unmarshal(raw, &options); err != nil {
			return nil, ipc.BadParam(fmt.Errorf("invalid session asset options: %w", err))
		}
	}
	var transport base.Transport
	var err error
	switch asset.Kind {
	case session.KindLocal:
		transport = local.NewWithConfig(local.Config{Shell: options.Shell, CWD: options.CWD, Env: options.Env})
	case session.KindSSH, session.KindDocker:
		transport, err = c.connectSSH(ctx, asset.ID, options, generation)
	case session.KindWinRM:
		transport, err = c.connectWinRM(ctx, options)
	default:
		err = fmt.Errorf("%w: %s transport", session.ErrUnsupported, asset.Kind)
	}
	if err != nil {
		return nil, err
	}
	if options.InitialCommand != "" {
		execCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, _ = transport.Exec(execCtx, options.InitialCommand, base.ExecOptions{})
		cancel()
		if err := ctx.Err(); err != nil {
			_ = transport.Close()
			return nil, err
		}
	}
	return transport, nil
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
	return ssh.Connect(ctx, config)
}

func (c *productionConnector) sshConfig(ctx context.Context, options productionAssetOptions, generation uint64, chain []string) (ssh.Config, error) {
	if options.Host == "" {
		return ssh.Config{}, ipc.BadParam(fmt.Errorf("SSH asset host is required"))
	}
	if options.Port == 0 {
		options.Port = 22
	}
	if options.Username == "" {
		options.Username = "root"
	}
	config := ssh.Config{
		Host: options.Host, Port: options.Port, User: options.Username, HostKeys: c.hostKeys,
		ProxyURL: options.Proxy, ProxyCommand: options.ProxyCommand, ForwardAgent: options.ForwardAgent,
		AutoAcceptUnknown: generation == 1 && options.AutoAcceptUnknownHost,
		ConnectTimeout:    time.Duration(options.ConnectTimeout) * time.Second,
	}
	switch options.AuthKind {
	case "password":
		secret, _, err := c.secret(ctx, options.CredID)
		if err != nil {
			return ssh.Config{}, err
		}
		config.Auth = ssh.AuthConfig{Method: ssh.AuthPassword, Password: secret}
	case "keyboard-interactive":
		secret, _, err := c.secret(ctx, options.CredID)
		if err != nil {
			return ssh.Config{}, err
		}
		config.Auth = ssh.AuthConfig{Method: ssh.AuthKeyboardInteractive, Password: secret}
	case "key":
		auth, err := c.keyAuth(ctx, options)
		if err != nil {
			return ssh.Config{}, err
		}
		config.Auth = auth
	case "agent":
		config.Auth = ssh.AuthConfig{Method: ssh.AuthAgent, AgentSocket: options.AgentSocket}
	default:
		return ssh.Config{}, ipc.BadParam(fmt.Errorf("SSH asset authentication method is required"))
	}
	if options.JumpAssetID != "" {
		jump, err := c.jumpConfig(ctx, options.JumpAssetID, generation, chain)
		if err != nil {
			return ssh.Config{}, err
		}
		config.Jump = &jump
	}
	return config, nil
}

func (c *productionConnector) jumpConfig(ctx context.Context, jumpAssetID string, generation uint64, chain []string) (ssh.Config, error) {
	for _, visited := range chain {
		if visited == jumpAssetID {
			return ssh.Config{}, ipc.BadParam(fmt.Errorf("SSH jump asset cycle: %s", strings.Join(append(chain, jumpAssetID), " -> ")))
		}
	}
	if len(chain) >= maxJumpAssetHops {
		return ssh.Config{}, ipc.BadParam(fmt.Errorf("SSH jump chain exceeds %d assets", maxJumpAssetHops))
	}
	row, err := c.database.AssetGet(ctx, jumpAssetID)
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
	return c.sshConfig(ctx, options, generation, append(chain, row.ID))
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

func productionAssetOptionsFromRow(row store.AssetRow) (productionAssetOptions, error) {
	var options productionAssetOptions
	merged, err := productionAssetOptionMap(row)
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
		return nil, ipc.BadParam(fmt.Errorf("WinRM asset host is required"))
	}
	if options.Port == 0 {
		options.Port = 5985
	}
	if options.Username == "" {
		options.Username = "Administrator"
	}
	secret, _, err := c.secret(ctx, options.CredID)
	if err != nil {
		return nil, err
	}
	return winrm.New(winrm.Config{
		Host: options.Host, Port: options.Port, User: options.Username, Password: secret,
		Domain: options.Domain, Auth: winrm.AuthMethod(options.Auth), UseTLS: options.TLS,
		AcceptInvalidCerts: options.AcceptInvalidCerts, TLSServerName: options.TLSServerName,
		CACert: []byte(options.CACert), ProxyURL: options.Proxy,
		RequestTimeout: time.Duration(options.RequestTimeoutMS) * time.Millisecond,
	})
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

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
