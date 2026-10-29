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
	runner        CommandRunner
	plistPath     string
	uid           string
	commandPrefix []string
}

func newLaunchdManager(runner CommandRunner, homeDir, uid string, commandPrefix ...string) ServiceManager {
	return newLaunchdManagerEnv(runner, nil, homeDir, uid, commandPrefix...)
}

func newLaunchdManagerEnv(runner CommandRunner, _ func(string) string, homeDir, uid string, commandPrefix ...string) *launchdManager {
	return &launchdManager{
		runner:        runner,
		plistPath:     filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist"),
		uid:           uid,
		commandPrefix: commandPrefix,
	}
}

func (m *launchdManager) domain() string {
	return "gui/" + m.uid
}

func (m *launchdManager) Install(ctx context.Context, executable, dataDir string) error {
	plist := renderLaunchdPlist(executable, dataDir, m.commandPrefix...)
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

// RecordedEnvironment reads a SHELL and PATH that an older install stored in the plist.
func (m *launchdManager) RecordedEnvironment() (ServiceEnvironment, bool) {
	data, err := os.ReadFile(m.plistPath)
	if err != nil {
		return ServiceEnvironment{}, false
	}
	var env ServiceEnvironment
	env.Shell = plistString(string(data), "SHELL")
	env.Path = plistString(string(data), "PATH")
	return env, true
}

func plistString(plist, key string) string {
	marker := "<key>" + key + "</key>\n    <string>"
	start := strings.Index(plist, marker)
	if start < 0 {
		return ""
	}
	rest := plist[start+len(marker):]
	end := strings.Index(rest, "</string>")
	if end < 0 {
		return ""
	}
	return xmlUnescape(rest[:end])
}

func xmlUnescape(value string) string {
	return strings.NewReplacer("&quot;", "\"", "&apos;", "'", "&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(value)
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

// launchdDisabled reports whether the print-disabled override database
// disables the agent label. It accepts both the macOS 13+ form
// ("label" => disabled|enabled) and the macOS 12 Monterey form
// ("label" => true|false), matching the full quoted label exactly.
func launchdDisabled(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "\""+launchdLabel+"\"") {
			continue
		}
		_, value, found := strings.Cut(line, "=>")
		if !found {
			continue
		}
		switch strings.TrimSpace(value) {
		case "disabled", "true":
			return true
		case "enabled", "false":
			return false
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

// renderLaunchdPlist renders the plist without EnvironmentVariables: the agent resolves the user's shell
// and environment at runtime, so nothing the installing process happened to have is recorded. Reinstalling
// therefore also drops values recorded by older versions.
func renderLaunchdPlist(executable, dataDir string, commandPrefix ...string) string {
	arguments := make([]string, 0, len(commandPrefix)+4)
	arguments = append(arguments, executable)
	arguments = append(arguments, commandPrefix...)
	arguments = append(arguments, "run", "--data-dir", dataDir)
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
