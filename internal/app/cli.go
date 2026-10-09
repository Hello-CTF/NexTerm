package app

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

type Command string

const (
	CommandDesktop Command = "desktop"
	CommandServe   Command = "serve"
)

const (
	AuthOn       = "on"
	AuthLoopback = "loopback"
	AuthPlatform = "platform"
	AuthOff      = "off"
)

type Invocation struct {
	Command        Command
	Listen         string
	DataDir        string
	WebRoot        string
	MasterKeyFile  string
	Auth           string
	DB             string
	DBDSN          string
	DBPasswordFile string
	DBMaxOpenConns int
	SyncOnly       bool
	RequireVault   bool
	Help           bool
	Version        bool
}

func ParseCLI(args []string, defaultCommand Command, getenv func(string) string) (Invocation, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	invocation := Invocation{
		Listen:         valueOr(getenv("NEXTERM_LISTEN"), "0.0.0.0:8080"),
		DataDir:        getenv("NEXTERM_DATA_DIR"),
		WebRoot:        getenv("NEXTERM_WEB_ROOT"),
		MasterKeyFile:  getenv("NEXTERM_MASTER_KEY_FILE"),
		Auth:           valueOr(getenv("NEXTERM_AUTH"), AuthOn),
		DB:             valueOr(getenv("NEXTERM_DB"), "sqlite"),
		DBDSN:          getenv("NEXTERM_DB_DSN"),
		DBPasswordFile: getenv("NEXTERM_DB_PASSWORD_FILE"),
	}
	dbMaxOpenConnsEnv := getenv("NEXTERM_DB_MAX_OPEN_CONNS")
	commandSeen := false

	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "-h" || argument == "--help" {
			invocation.Help = true
			continue
		}
		if argument == "--version" {
			invocation.Version = true
			continue
		}
		if argument == "--sync-only" {
			invocation.SyncOnly = true
			continue
		}
		if strings.HasPrefix(argument, "--sync-only=") {
			value, err := strconv.ParseBool(strings.TrimPrefix(argument, "--sync-only="))
			if err != nil {
				return Invocation{}, fmt.Errorf("invalid --sync-only value: %w", err)
			}
			invocation.SyncOnly = value
			continue
		}
		if argument == "--require-vault" {
			invocation.RequireVault = true
			continue
		}
		if strings.HasPrefix(argument, "--require-vault=") {
			value, err := strconv.ParseBool(strings.TrimPrefix(argument, "--require-vault="))
			if err != nil {
				return Invocation{}, fmt.Errorf("invalid --require-vault value: %w", err)
			}
			invocation.RequireVault = value
			continue
		}
		if strings.HasPrefix(argument, "--") {
			name, value, hasValue := strings.Cut(argument, "=")
			if !isStringFlag(name) {
				return Invocation{}, fmt.Errorf("unknown option %s", name)
			}
			if !hasValue {
				index++
				if index >= len(args) {
					return Invocation{}, fmt.Errorf("option %s requires a value", name)
				}
				value = args[index]
			}
			switch name {
			case "--listen":
				invocation.Listen = value
			case "--data-dir":
				invocation.DataDir = value
			case "--web-root":
				invocation.WebRoot = value
			case "--master-key-file":
				invocation.MasterKeyFile = value
			case "--auth":
				invocation.Auth = value
			case "--db":
				invocation.DB = value
			case "--db-dsn":
				invocation.DBDSN = value
			case "--db-password-file":
				invocation.DBPasswordFile = value
			case "--db-max-open-conns":
				parsed, err := strconv.Atoi(value)
				if err != nil || parsed <= 0 {
					return Invocation{}, fmt.Errorf("invalid --db-max-open-conns value %q (want a positive integer)", value)
				}
				invocation.DBMaxOpenConns = parsed
			}
			continue
		}

		command := Command(argument)
		if !validCommand(command) {
			return Invocation{}, fmt.Errorf("unknown command %q", argument)
		}
		if commandSeen {
			return Invocation{}, fmt.Errorf("multiple commands provided")
		}
		invocation.Command = command
		commandSeen = true
	}

	if !commandSeen {
		if !validCommand(defaultCommand) {
			return Invocation{}, fmt.Errorf("invalid default command %q", defaultCommand)
		}
		invocation.Command = defaultCommand
	}
	if !invocation.Help && !invocation.Version {
		if err := ValidateListenAddress(invocation.Listen); err != nil {
			return Invocation{}, err
		}
		switch invocation.Auth {
		case AuthOn, AuthLoopback, AuthPlatform, AuthOff:
		default:
			return Invocation{}, fmt.Errorf("invalid --auth value %q (want %q, %q, %q, or %q)", invocation.Auth, AuthOn, AuthLoopback, AuthPlatform, AuthOff)
		}
		backend, err := store.ParseBackend(invocation.DB)
		if err != nil {
			return Invocation{}, err
		}
		if invocation.DBMaxOpenConns == 0 && dbMaxOpenConnsEnv != "" {
			parsed, err := strconv.Atoi(dbMaxOpenConnsEnv)
			if err != nil || parsed <= 0 {
				return Invocation{}, fmt.Errorf("invalid NEXTERM_DB_MAX_OPEN_CONNS value %q (want a positive integer)", dbMaxOpenConnsEnv)
			}
			invocation.DBMaxOpenConns = parsed
		}
		if backend == store.BackendPostgres && invocation.DBDSN == "" {
			return Invocation{}, fmt.Errorf("postgres backend requires --db-dsn (env NEXTERM_DB_DSN)")
		}
		if backend == store.BackendSQLite && (invocation.DBDSN != "" || invocation.DBPasswordFile != "") {
			return Invocation{}, fmt.Errorf("--db-dsn and --db-password-file require --db=postgres")
		}
	}
	return invocation, nil
}

func ValidateListenAddress(address string) error {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", address, err)
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 0 || value > 65535 {
		return fmt.Errorf("invalid listen port %q", port)
	}
	return nil
}

func Usage(program string, defaultCommand Command) string {
	return fmt.Sprintf(`Usage:
  %s [flags] [%s]

Commands:
  desktop       Run the Wails desktop application
  serve         Run the HTTP server (default for nexterm-server)

Flags:
  --listen ADDRESS    HTTP listen address (env NEXTERM_LISTEN)
  --data-dir PATH     Data directory (env NEXTERM_DATA_DIR)
  --web-root PATH     Web assets directory (env NEXTERM_WEB_ROOT)
  --master-key-file PATH  Read the vault master key from a file (env NEXTERM_MASTER_KEY_FILE)
  --auth MODE         Access control: on, loopback, platform, or off (env NEXTERM_AUTH, default on)
                      on = account sessions; loopback = no auth on a loopback listener;
                      platform = access control delegated to a fronting gateway: only requests
                      carrying the NEXTERM_GATEWAY_AUTH header are admitted, and the first such
                      request becomes the platform owner account (no init code);
                      off = local shared workspace only: account/admin/device routes stay
                      closed (no implicit superadmin) and startup refuses if any user exists
  --db BACKEND        Server database backend: sqlite or postgres (env NEXTERM_DB, default sqlite);
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
`, program, defaultCommand)
}

func isStringFlag(name string) bool {
	switch name {
	case "--listen", "--data-dir", "--web-root", "--master-key-file", "--auth",
		"--db", "--db-dsn", "--db-password-file", "--db-max-open-conns":
		return true
	default:
		return false
	}
}

func validCommand(command Command) bool {
	switch command {
	case CommandDesktop, CommandServe:
		return true
	default:
		return false
	}
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
