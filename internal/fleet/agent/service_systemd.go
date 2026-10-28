package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/usershell"
)

const systemdUnitName = "nexterm-agent.service"

type systemdManager struct {
	runner        CommandRunner
	unitPath      string
	commandPrefix []string
	getenv        func(string) string
	lookupShell   func() string
}

func newSystemdManager(runner CommandRunner, getenv func(string) string, homeDir string, commandPrefix ...string) ServiceManager {
	configHome := getenv("XDG_CONFIG_HOME")
	if configHome == "" && homeDir != "" {
		configHome = filepath.Join(homeDir, ".config")
	}
	return &systemdManager{
		runner:        runner,
		unitPath:      filepath.Join(configHome, "systemd", "user", systemdUnitName),
		commandPrefix: commandPrefix,
		getenv:        getenv,
		lookupShell:   usershell.Lookup,
	}
}

func (m *systemdManager) Install(ctx context.Context, executable, dataDir string) error {
	unit := renderSystemdUnitEnv(executable, dataDir, captureServiceEnvironment(m.getenv, m.lookupShell), m.commandPrefix...)
	if err := os.MkdirAll(filepath.Dir(m.unitPath), 0o755); err != nil {
		return fmt.Errorf("create systemd user directory: %w", err)
	}
	if err := os.WriteFile(m.unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}
	if _, err := m.runner.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemd daemon-reload: %w", err)
	}
	if _, err := m.runner.Run(ctx, "systemctl", "--user", "enable", "--now", systemdUnitName); err != nil {
		return fmt.Errorf("systemd enable --now: %w", err)
	}
	return nil
}

func (m *systemdManager) Uninstall(ctx context.Context) error {
	if _, err := os.Stat(m.unitPath); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if _, err := m.runner.Run(ctx, "systemctl", "--user", "disable", "--now", systemdUnitName); err != nil {
		return fmt.Errorf("systemd disable --now: %w", err)
	}
	if err := os.Remove(m.unitPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove systemd unit: %w", err)
	}
	if _, err := m.runner.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemd daemon-reload: %w", err)
	}
	return nil
}

func (m *systemdManager) Status(ctx context.Context) ServiceState {
	status := ServiceState{}
	if _, err := os.Stat(m.unitPath); err == nil {
		status.Installed = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		status.LastError = fmt.Sprintf("stat systemd unit: %v", err)
		return status
	}
	output, err := m.runner.Run(ctx, "systemctl", "--user", "is-enabled", systemdUnitName)
	if state, failure := systemdQuery(output, err); failure != "" {
		status.LastError = failure
		return status
	} else {
		status.Enabled = state == "enabled"
	}
	output, err = m.runner.Run(ctx, "systemctl", "--user", "is-active", systemdUnitName)
	if state, failure := systemdQuery(output, err); failure != "" {
		status.LastError = failure
		return status
	} else {
		status.Active = state == "active"
	}
	return status
}

var systemdStateWords = map[string]bool{
	"enabled": true, "disabled": true, "static": true, "masked": true,
	"indirect": true, "generated": true, "transient": true,
	"active": true, "inactive": true, "failed": true, "activating": true,
	"deactivating": true, "maintenance": true, "reloading": true,
}

// systemdQuery maps an is-enabled/is-active invocation to (state, failure).
// A printed unit state and a missing unit file are normal results; anything
// else, such as an unreachable user bus, is an honest failure carrying the
// command output.
func systemdQuery(output string, err error) (string, string) {
	state := trimOutput(output)
	if err == nil {
		return state, ""
	}
	if systemdStateWords[state] || strings.HasPrefix(state, "Failed to get unit file state") {
		return "", ""
	}
	if state == "" {
		return "", err.Error()
	}
	return "", state
}

func renderSystemdUnit(executable, dataDir string, commandPrefix ...string) string {
	return renderSystemdUnitEnv(executable, dataDir, ServiceEnvironment{}, commandPrefix...)
}

func renderSystemdUnitEnv(executable, dataDir string, env ServiceEnvironment, commandPrefix ...string) string {
	command := systemdEscape(executable)
	if len(commandPrefix) > 0 {
		command += " " + strings.Join(commandPrefix, " ")
	}
	command += " run --data-dir " + systemdEscape(dataDir)
	var environment string
	if env.Shell != "" {
		environment += "Environment=" + systemdEnvAssignment("SHELL", env.Shell) + "\n"
	}
	if env.Path != "" {
		environment += "Environment=" + systemdEnvAssignment("PATH", env.Path) + "\n"
	}
	return "[Unit]\n" +
		"Description=NexTerm Device Agent\n" +
		"After=network-online.target\n" +
		"\n" +
		"[Service]\n" +
		"ExecStart=" + command + "\n" +
		environment +
		"Restart=on-failure\n" +
		"RestartSec=5\n" +
		"RestartPreventExitStatus=" + fmt.Sprintf("%d %d", ExitRevoked, ExitProtocol) + "\n" +
		"\n" +
		"[Install]\n" +
		"WantedBy=default.target\n"
}

func systemdEscape(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"\"", "\\\"",
		"%", "%%",
		"$", "$$",
	)
	return "\"" + replacer.Replace(value) + "\""
}

// systemdEnvAssignment quotes KEY=value for Environment=. Only backslash, double quote and % need escaping;
// $ is not expanded in Environment= values.
func systemdEnvAssignment(key, value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "%", "%%")
	return "\"" + key + "=" + replacer.Replace(value) + "\""
}

// RecordedEnvironment reads the SHELL and PATH stored in the installed unit.
func (m *systemdManager) RecordedEnvironment() (ServiceEnvironment, bool) {
	data, err := os.ReadFile(m.unitPath)
	if err != nil {
		return ServiceEnvironment{}, false
	}
	var env ServiceEnvironment
	unquote := strings.NewReplacer("%%", "%", "\\\"", "\"", "\\\\", "\\")
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "Environment=\"")
		if !ok || !strings.HasSuffix(rest, "\"") {
			continue
		}
		rest = unquote.Replace(strings.TrimSuffix(rest, "\""))
		switch {
		case strings.HasPrefix(rest, "SHELL="):
			env.Shell = strings.TrimPrefix(rest, "SHELL=")
		case strings.HasPrefix(rest, "PATH="):
			env.Path = strings.TrimPrefix(rest, "PATH=")
		}
	}
	finishRecorded(&env)
	return env, true
}
