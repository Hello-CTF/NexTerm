package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/platform"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

const DefaultListen = "0.0.0.0:8080"

type Command = core.Command

const (
	CommandServe       = core.CommandServe
	CommandToken       = core.CommandToken
	CommandRotateToken = core.CommandRotateToken
)

const (
	AuthOn       = core.AuthOn
	AuthLoopback = core.AuthLoopback
	AuthOff      = core.AuthOff
)

type Options struct {
	Listen         string
	DataDir        string
	WebRoot        string
	MasterKey      string
	SyncOnly       bool
	Auth           string
	AllowedOrigins []string
}

type Invocation struct {
	Command core.Command
	Options Options
	Help    bool
	Version bool
}

type TokenStore = core.TokenStore

func ParseCLI(args []string, getenv func(string) string) (Invocation, error) {
	parsed, err := core.ParseCLI(args, core.CommandServe, getenv)
	if err != nil {
		return Invocation{}, err
	}
	if parsed.Command == core.CommandDesktop {
		return Invocation{}, fmt.Errorf("desktop command is not available in nexterm-server")
	}

	dataDir := parsed.DataDir
	if dataDir == "" && !parsed.Help && !parsed.Version {
		paths, err := platform.ServerPaths("")
		if err != nil {
			return Invocation{}, err
		}
		dataDir = paths.DataDir
	}
	webRoot := parsed.WebRoot
	if webRoot == "" && !parsed.SyncOnly && !parsed.Help && !parsed.Version {
		webRoot = ProbeWebRoot()
	}
	masterKey := parsed.MasterKey
	if !parsed.Help && !parsed.Version {
		masterKey, err = ResolveMasterKey(parsed.MasterKey, parsed.MasterKeyFile)
		if err != nil {
			return Invocation{}, err
		}
	}
	return Invocation{
		Command: parsed.Command,
		Options: Options{
			Listen: parsed.Listen, DataDir: dataDir, WebRoot: webRoot,
			MasterKey: masterKey, SyncOnly: parsed.SyncOnly, Auth: parsed.Auth,
		},
		Help: parsed.Help, Version: parsed.Version,
	}, nil
}

func ResolveMasterKey(masterKey, masterKeyFile string) (string, error) {
	if masterKeyFile == "" {
		return masterKey, nil
	}
	if masterKey != "" {
		return "", fmt.Errorf("--master-key and --master-key-file cannot be combined")
	}
	data, err := os.ReadFile(masterKeyFile)
	if err != nil {
		return "", fmt.Errorf("read master key file: %w", err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("master key file %s is empty", masterKeyFile)
	}
	return key, nil
}

func Usage(program string) string {
	return fmt.Sprintf(`Usage:
  %s [flags] [serve]

Commands:
  serve         Run the HTTP server (default)
  token         Print the current sync token to stdout
  rotate-token  Rotate and print the sync token to stdout

Flags:
  --listen ADDRESS    HTTP listen address (env NEXTERM_LISTEN)
  --data-dir PATH     Data directory (env NEXTERM_DATA_DIR)
  --web-root PATH     Web assets directory (env NEXTERM_WEB_ROOT)
  --master-key KEY    Vault master key (env NEXTERM_MASTER_KEY, deprecated; use --master-key-file)
  --master-key-file PATH  Read the vault master key from a file (env NEXTERM_MASTER_KEY_FILE)
  --auth MODE         Access control: on, loopback, or off (env NEXTERM_AUTH, default on)
  --require-vault     Fail startup unless the credential vault unlocks
  --sync-only         Restrict the server to sync routes
  --version           Print the version
  -h, --help          Print this help
`, program)
}

func RunTokenCommand(ctx context.Context, command Command, store TokenStore, stdout io.Writer) error {
	return core.RunTokenCommand(ctx, command, store, stdout)
}

func ProbeWebRoot() string {
	for _, candidate := range []string{"/app/dist", "dist", "../dist"} {
		if info, err := os.Stat(filepath.Join(candidate, "index.html")); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return "/app/dist"
}

type Vault interface {
	Status() vault.Status
	InitMaster(context.Context, string) error
	UnlockMaster(context.Context, string) error
}

func BootstrapVault(ctx context.Context, credentialVault Vault, masterKey string) error {
	if masterKey == "" {
		return nil
	}
	if credentialVault == nil {
		return fmt.Errorf("vault is not configured")
	}
	if len(masterKey) < 8 {
		return fmt.Errorf("master key must contain at least 8 bytes")
	}
	if credentialVault.Status().Initialized {
		return credentialVault.UnlockMaster(ctx, masterKey)
	}
	return credentialVault.InitMaster(ctx, masterKey)
}

func BootstrapVaultRequired(ctx context.Context, credentialVault Vault, masterKey string) error {
	if masterKey == "" {
		return fmt.Errorf("vault is required but no master key is configured")
	}
	if err := BootstrapVault(ctx, credentialVault, masterKey); err != nil {
		return fmt.Errorf("vault bootstrap failed: %w", err)
	}
	if !credentialVault.Status().Unlocked {
		return fmt.Errorf("vault is required but remained locked after bootstrap")
	}
	return nil
}

func warnIfExposed(logger *slog.Logger, stderr io.Writer, address string, syncOnly bool) {
	if core.LoopbackListen(address) {
		return
	}

	detail := "完整版已启用访问控制：/rpc、/ws 与 /files/blob 要求同步令牌（浏览器打开时会提示输入，可用 nexterm-server token 查看、rotate-token 轮换），/healthz 与页面静态资源保持公开。公网部署仍建议套 TLS 反向代理并用防火墙限制来源地址。"
	if syncOnly {
		detail = "onlyServer 模式：/sync/rpc 要求同步令牌；能连接此端口且持有同步令牌的人可以读写资产库（含密码类凭据）。请使用防火墙限制对端地址。"
	}
	if logger != nil {
		logger.Warn("HTTP server is listening on a non-loopback address", "listen", address, "syncOnly", syncOnly, "risk", detail)
	}
	if stderr != nil {
		_, _ = fmt.Fprintf(stderr, "WARNING: NexTerm is listening on non-loopback address %s. %s\n", address, detail)
	}
}
