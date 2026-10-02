package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/platform"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

const DefaultListen = "0.0.0.0:8080"

type Options struct {
	Listen    string
	DataDir   string
	WebRoot   string
	MasterKey string
	SyncOnly  bool
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
	return Invocation{
		Command: parsed.Command,
		Options: Options{
			Listen: parsed.Listen, DataDir: dataDir, WebRoot: webRoot,
			MasterKey: parsed.MasterKey, SyncOnly: parsed.SyncOnly,
		},
		Help: parsed.Help, Version: parsed.Version,
	}, nil
}

func Usage(program string) string {
	return core.Usage(program, core.CommandServe)
}

func RunTokenCommand(ctx context.Context, command core.Command, store TokenStore, stdout io.Writer) error {
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

func warnIfExposed(logger *slog.Logger, stderr io.Writer, address string, syncOnly bool) {
	host, _, err := net.SplitHostPort(address)
	if err == nil {
		if host == "localhost" {
			return
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return
		}
	}

	detail := "完整版的 /rpc 与浏览器界面没有自身鉴权；能连接此端口的人就拥有终端、文件、Docker 与凭据库的完整控制权。公网部署请使用 --sync-only，或只监听回环地址并由带鉴权的反向代理转发。"
	if syncOnly {
		detail = "onlyServer 模式：能连接此端口且持有同步令牌的人可以读写资产库（含密码类凭据）。请使用防火墙限制对端地址。"
	}
	if logger != nil {
		logger.Warn("HTTP server is listening on a non-loopback address", "listen", address, "syncOnly", syncOnly, "risk", detail)
	}
	if stderr != nil {
		_, _ = fmt.Fprintf(stderr, "WARNING: NexTerm is listening on non-loopback address %s. %s\n", address, detail)
	}
}
