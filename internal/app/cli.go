package app

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type Command string

const (
	CommandDesktop     Command = "desktop"
	CommandServe       Command = "serve"
	CommandToken       Command = "token"
	CommandRotateToken Command = "rotate-token"
)

const (
	AuthOn       = "on"
	AuthLoopback = "loopback"
	AuthOff      = "off"
)

type Invocation struct {
	Command       Command
	Listen        string
	DataDir       string
	WebRoot       string
	MasterKey     string
	MasterKeyFile string
	Auth          string
	PublicBaseURL string
	SyncOnly      bool
	RequireVault  bool
	Help          bool
	Version       bool
}

// FilePublicBaseURLSettingKey 是持久化在 setting 表的文件访问基础 URL 键,
// 服务端 admin settings API 与桌面 files IPC 共用。
const FilePublicBaseURLSettingKey = "files.public_base_url"

// ParsePublicBaseURL 校验文件访问基础 URL: 仅接受 http/https, 必须有 host,
// 允许路径前缀, 拒绝 userinfo/query/fragment; 返回去掉尾斜杠的规范形式。
// 空串表示未设置(调用方据此回退为同源相对链接)。
func ParsePublicBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid public base URL %q: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("invalid public base URL %q: scheme must be http or https", raw)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid public base URL %q: host is required", raw)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("invalid public base URL %q: userinfo is not allowed", raw)
	}
	if parsed.RawQuery != "" {
		return "", fmt.Errorf("invalid public base URL %q: query is not allowed", raw)
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("invalid public base URL %q: fragment is not allowed", raw)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

func ParseCLI(args []string, defaultCommand Command, getenv func(string) string) (Invocation, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	invocation := Invocation{
		Listen:        valueOr(getenv("NEXTERM_LISTEN"), "0.0.0.0:8080"),
		DataDir:       getenv("NEXTERM_DATA_DIR"),
		WebRoot:       getenv("NEXTERM_WEB_ROOT"),
		MasterKey:     getenv("NEXTERM_MASTER_KEY"),
		MasterKeyFile: getenv("NEXTERM_MASTER_KEY_FILE"),
		Auth:          valueOr(getenv("NEXTERM_AUTH"), AuthOn),
		PublicBaseURL: getenv("NEXTERM_PUBLIC_BASE_URL"),
	}
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
			case "--master-key":
				invocation.MasterKey = value
			case "--master-key-file":
				invocation.MasterKeyFile = value
			case "--auth":
				invocation.Auth = value
			case "--public-base-url":
				invocation.PublicBaseURL = value
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
		case AuthOn, AuthLoopback, AuthOff:
		default:
			return Invocation{}, fmt.Errorf("invalid --auth value %q (want %q, %q, or %q)", invocation.Auth, AuthOn, AuthLoopback, AuthOff)
		}
		base, err := ParsePublicBaseURL(invocation.PublicBaseURL)
		if err != nil {
			return Invocation{}, err
		}
		invocation.PublicBaseURL = base
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
  token         Print the current sync token to stdout
  rotate-token  Rotate and print the sync token to stdout

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
  --require-vault     Fail startup unless the credential vault unlocks
  --sync-only         Restrict the server to sync routes
  --version           Print the version
  -h, --help          Print this help
`, program, defaultCommand)
}

func isStringFlag(name string) bool {
	switch name {
	case "--listen", "--data-dir", "--web-root", "--master-key", "--master-key-file", "--auth", "--public-base-url":
		return true
	default:
		return false
	}
}

func validCommand(command Command) bool {
	switch command {
	case CommandDesktop, CommandServe, CommandToken, CommandRotateToken:
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
