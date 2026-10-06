package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const launchdLabel = "com.nexterm.agent"

type launchdManager struct {
	runner    CommandRunner
	plistPath string
	uid       string
}

func newLaunchdManager(runner CommandRunner, homeDir, uid string) ServiceManager {
	return &launchdManager{
		runner:    runner,
		plistPath: filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist"),
		uid:       uid,
	}
}

func (m *launchdManager) domain() string {
	return "gui/" + m.uid
}

func (m *launchdManager) Install(ctx context.Context, executable, dataDir string) error {
	plist := renderLaunchdPlist(executable, dataDir)
	if err := os.MkdirAll(filepath.Dir(m.plistPath), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents directory: %w", err)
	}
	if err := os.WriteFile(m.plistPath, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write launchd plist: %w", err)
	}
	_, _ = m.runner.Run(ctx, "launchctl", "bootout", m.domain()+"/"+launchdLabel)
	if _, err := m.runner.Run(ctx, "launchctl", "bootstrap", m.domain(), m.plistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}
	if _, err := m.runner.Run(ctx, "launchctl", "enable", m.domain()+"/"+launchdLabel); err != nil {
		return fmt.Errorf("launchctl enable: %w", err)
	}
	return nil
}

func (m *launchdManager) Uninstall(ctx context.Context) error {
	if _, err := os.Stat(m.plistPath); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if _, err := m.runner.Run(ctx, "launchctl", "bootout", m.domain()+"/"+launchdLabel); err != nil && !isExitError(err) {
		return fmt.Errorf("launchctl bootout: %w", err)
	}
	if err := os.Remove(m.plistPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove launchd plist: %w", err)
	}
	return nil
}

func (m *launchdManager) Status(ctx context.Context) ServiceState {
	status := ServiceState{}
	if _, err := os.Stat(m.plistPath); err == nil {
		status.Installed = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		status.LastError = fmt.Sprintf("stat launchd plist: %v", err)
		return status
	}
	if _, err := m.runner.Run(ctx, "launchctl", "print", m.domain()+"/"+launchdLabel); err != nil {
		if !isExitError(err) {
			status.LastError = err.Error()
		}
		return status
	}
	status.Active = true
	status.Enabled = true
	output, err := m.runner.Run(ctx, "launchctl", "print-disabled", m.domain())
	if err != nil {
		if !isExitError(err) {
			status.LastError = err.Error()
		}
		return status
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, launchdLabel) && strings.Contains(line, "disabled") {
			status.Enabled = false
		}
	}
	return status
}

func renderLaunchdPlist(executable, dataDir string) string {
	arguments := []string{executable, "agent", "run", "--data-dir", dataDir}
	var builder strings.Builder
	builder.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	builder.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	builder.WriteString("<plist version=\"1.0\">\n<dict>\n")
	builder.WriteString("  <key>Label</key>\n  <string>" + launchdLabel + "</string>\n")
	builder.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, argument := range arguments {
		builder.WriteString("    <string>" + xmlEscape(argument) + "</string>\n")
	}
	builder.WriteString("  </array>\n")
	builder.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	builder.WriteString("  <key>KeepAlive</key>\n  <true/>\n")
	builder.WriteString("</dict>\n</plist>\n")
	return builder.String()
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(value)
}
