package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/platform"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

const DefaultListen = "0.0.0.0:8080"

type Command = core.Command

const CommandServe = core.CommandServe

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
	PublicBaseURL  string
	AllowedOrigins []string
}

type Invocation struct {
	Command core.Command
	Options Options
	Help    bool
	Version bool
}

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
			PublicBaseURL: parsed.PublicBaseURL,
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

func ResolveDBPassword(passwordFile string) (string, error) {
	if passwordFile == "" {
		return "", nil
	}
	info, err := os.Stat(passwordFile)
	if err != nil {
		return "", fmt.Errorf("stat database password file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("database password file %s is not a regular file", passwordFile)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("database password file %s must have 0600 permissions (got %04o)", passwordFile, info.Mode().Perm())
	}
	data, err := os.ReadFile(passwordFile)
	if err != nil {
		return "", fmt.Errorf("read database password file: %w", err)
	}
	password := strings.TrimSpace(string(data))
	if password == "" {
		return "", fmt.Errorf("database password file %s is empty", passwordFile)
	}
	return password, nil
}

func Usage(program string) string {
	return fmt.Sprintf(`Usage:
  %s [flags] [serve]

Commands:
  serve         Run the HTTP server (default)

Flags:
  --listen ADDRESS    HTTP listen address (env NEXTERM_LISTEN)
  --data-dir PATH     Data directory (env NEXTERM_DATA_DIR)
  --web-root PATH     Web assets directory (env NEXTERM_WEB_ROOT)
  --master-key KEY    Vault master key (env NEXTERM_MASTER_KEY, deprecated; use --master-key-file)
  --master-key-file PATH  Read the vault master key from a file (env NEXTERM_MASTER_KEY_FILE)
  --auth MODE         Access control: on, loopback, or off (env NEXTERM_AUTH, default on)
                      on = account sessions; loopback = no auth on a loopback listener;
                      off = local shared workspace only: account/admin/device routes stay
                      closed (no implicit superadmin) and startup refuses if any user exists
  --public-base-url URL  Default file access base URL for public image links
                      (env NEXTERM_PUBLIC_BASE_URL); http/https only, path prefix allowed,
                      userinfo/query/fragment rejected; unset = same-origin relative links
  --db BACKEND        Database backend: sqlite or postgres (env NEXTERM_DB, default sqlite);
                      postgres runs the embedded migrations/postgres schema and, in this first
                      version, supports a single writer instance only
  --db-dsn DSN        Postgres connection string (env NEXTERM_DB_DSN), e.g.
                      postgres://user@host:5432/nexterm?sslmode=verify-full&sslrootcert=/path/ca.crt;
                      requires --db=postgres; the password may come from --db-password-file instead
  --db-password-file PATH  Read the postgres password from a 0600 file
                      (env NEXTERM_DB_PASSWORD_FILE); rejected when the DSN already has a password
  --db-max-open-conns N  Postgres pool size (env NEXTERM_DB_MAX_OPEN_CONNS, default 16)
  --require-vault     Fail startup unless the credential vault unlocks
  --sync-only         Restrict the server to sync routes
  --version           Print the version
  -h, --help          Print this help
`, program)
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
