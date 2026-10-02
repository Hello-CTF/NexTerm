package app

import (
	"fmt"
	"net"
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

type Invocation struct {
	Command   Command
	Listen    string
	DataDir   string
	WebRoot   string
	MasterKey string
	SyncOnly  bool
	Help      bool
	Version   bool
}

func ParseCLI(args []string, defaultCommand Command, getenv func(string) string) (Invocation, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	invocation := Invocation{
		Listen:    valueOr(getenv("NEXTERM_LISTEN"), "0.0.0.0:8080"),
		DataDir:   getenv("NEXTERM_DATA_DIR"),
		WebRoot:   getenv("NEXTERM_WEB_ROOT"),
		MasterKey: getenv("NEXTERM_MASTER_KEY"),
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
  --master-key KEY    Vault master key (env NEXTERM_MASTER_KEY)
  --sync-only         Restrict the server to sync routes
  --version           Print the version
  -h, --help          Print this help
`, program, defaultCommand)
}

func isStringFlag(name string) bool {
	switch name {
	case "--listen", "--data-dir", "--web-root", "--master-key":
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
