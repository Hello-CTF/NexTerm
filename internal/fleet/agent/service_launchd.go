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
	if output, err := m.runner.Run(ctx, "launchctl", "bootout", m.domain()+"/"+launchdLabel); err != nil && !strings.Contains(trimOutput(output), "Could not find service") {
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
	output, err := m.runner.Run(ctx, "launchctl", "print", m.domain()+"/"+launchdLabel)
	trimmed := trimOutput(output)
	switch {
	case err == nil:
		status.Active = launchdState(trimmed) == "running"
	case strings.Contains(trimmed, "Could not find service"):
	default:
		status.LastError = launchdFailure(trimmed, err)
		return status
	}
	output, err = m.runner.Run(ctx, "launchctl", "print-disabled", m.domain())
	if err != nil {
		status.LastError = launchdFailure(trimOutput(output), err)
		return status
	}
	status.Enabled = !launchdDisabled(trimOutput(output))
	return status
}

func launchdState(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "state = ") {
			return strings.TrimPrefix(line, "state = ")
		}
	}
	return ""
}

func launchdDisabled(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, launchdLabel) && strings.Contains(line, "=> disabled") {
			return true
		}
	}
	return false
}

func launchdFailure(output string, err error) string {
	if output != "" {
		return output
	}
	return err.Error()
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
	builder.WriteString("  <key>KeepAlive</key>\n  <dict>\n    <key>Crashed</key>\n    <true/>\n  </dict>\n")
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
