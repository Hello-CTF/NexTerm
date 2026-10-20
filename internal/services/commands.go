package services

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	ActionStatus  = "status"
	ActionStart   = "start"
	ActionStop    = "stop"
	ActionRestart = "restart"
	ActionReload  = "reload"
	ActionEnable  = "enable"
	ActionDisable = "disable"
)

func SystemdListCommand(sudo bool) string {
	return optionalSudo("systemctl list-units --type=service --all --no-legend --no-pager", sudo)
}

func SystemdActionCommand(action, unit string, sudo bool) (string, error) {
	if err := validateUnit(unit); err != nil {
		return "", err
	}
	quoted := quoteShell(unit)
	var command string
	switch action {
	case ActionStatus:
		command = "systemctl status --no-pager -- " + quoted
	case ActionStart, ActionStop, ActionRestart, ActionReload, ActionEnable, ActionDisable:
		command = "systemctl " + action + " -- " + quoted
	default:
		return "", fmt.Errorf("%w: unsupported systemctl action %q", ErrInvalid, action)
	}
	return optionalSudo(command, sudo), nil
}

func KillCommand(pid int, sudo bool) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("%w: pid must be a positive integer", ErrInvalid)
	}
	return optionalSudo("kill -TERM -- "+strconv.Itoa(pid), sudo), nil
}

func FinderCommand(sudo bool) string {
	listeners := optionalSudo("ss -ltnp 2>&1", sudo)
	netstat := optionalSudo("netstat -ltnp 2>&1", sudo)
	processes := optionalSudo("ps -eo pid,user,%cpu,%mem,etime,args 2>&1", sudo)
	parts := []string{
		"printf '%s\\n' '__NEXTERM_LISTENER_BEGIN__'",
		"if command -v ss >/dev/null 2>&1; then printf '%s\\n' '__NEXTERM_TOOL__ss'; " + listeners + "; printf '__NEXTERM_LISTENER_END__:%s\\n' \"$?\"",
		"elif command -v netstat >/dev/null 2>&1; then printf '%s\\n' '__NEXTERM_TOOL__netstat'; " + netstat + "; printf '__NEXTERM_LISTENER_END__:%s\\n' \"$?\"",
		"else printf '%s\\n' '__NEXTERM_LISTENER_ERROR__missing ss and netstat'; printf '%s\\n' '__NEXTERM_LISTENER_END__:127'; fi",
		"printf '%s\\n' '__NEXTERM_PS_BEGIN__'",
		"if command -v ps >/dev/null 2>&1; then " + processes + "; printf '__NEXTERM_PS_END__:%s\\n' \"$?\"",
		"else printf '%s\\n' '__NEXTERM_PS_ERROR__missing ps'; printf '%s\\n' '__NEXTERM_PS_END__:127'; fi",
		"exit 0",
	}
	return strings.Join(parts, "; ")
}

func optionalSudo(command string, sudo bool) string {
	if !sudo {
		return command
	}
	return "if [ \"$(id -u)\" -eq 0 ]; then " + command + "; elif command -v sudo >/dev/null 2>&1; then sudo -n -- " + command + "; else printf '%s\\n' 'sudo is unavailable and no password can be requested'; (exit 127); fi"
}

func validateUnit(unit string) error {
	if unit == "" || len(unit) > 1024 || strings.TrimSpace(unit) != unit || strings.ContainsAny(unit, "\x00\r\n") {
		return fmt.Errorf("%w: unit must be one non-empty line of at most 1024 bytes", ErrInvalid)
	}
	return nil
}

func quoteShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
