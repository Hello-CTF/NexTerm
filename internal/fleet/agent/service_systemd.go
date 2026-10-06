package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const systemdUnitName = "nexterm-agent.service"

type systemdManager struct {
	runner   CommandRunner
	unitPath string
}

func newSystemdManager(runner CommandRunner, getenv func(string) string, homeDir string) ServiceManager {
	configHome := getenv("XDG_CONFIG_HOME")
	if configHome == "" && homeDir != "" {
		configHome = filepath.Join(homeDir, ".config")
	}
	return &systemdManager{runner: runner, unitPath: filepath.Join(configHome, "systemd", "user", systemdUnitName)}
}

func (m *systemdManager) Install(ctx context.Context, executable, dataDir string) error {
	unit := renderSystemdUnit(executable, dataDir)
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
	if err != nil {
		if !isExitError(err) {
			status.LastError = err.Error()
		}
		return status
	}
	status.Enabled = trimOutput(output) == "enabled"
	output, err = m.runner.Run(ctx, "systemctl", "--user", "is-active", systemdUnitName)
	if err != nil {
		if !isExitError(err) {
			status.LastError = err.Error()
		}
		return status
	}
	status.Active = trimOutput(output) == "active"
	return status
}

func isExitError(err error) bool {
	var exitError *exec.ExitError
	return errors.As(err, &exitError)
}

func renderSystemdUnit(executable, dataDir string) string {
	return "[Unit]\n" +
		"Description=NexTerm Device Agent\n" +
		"After=network-online.target\n" +
		"\n" +
		"[Service]\n" +
		"ExecStart=" + systemdEscape(executable) + " agent run --data-dir " + systemdEscape(dataDir) + "\n" +
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
