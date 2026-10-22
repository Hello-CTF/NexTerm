package services

import (
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

func ParseSystemdUnits(output string) []ServiceUnit {
	units := make([]ServiceUnit, 0)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSuffix(line, "\r"))
		if len(fields) < 4 || fields[0] == "UNIT" {
			continue
		}
		if !strings.HasSuffix(fields[0], ".service") && len(fields) >= 5 && strings.HasSuffix(fields[1], ".service") {
			fields = fields[1:]
		}
		if len(fields) < 4 || !strings.HasSuffix(fields[0], ".service") {
			continue
		}
		units = append(units, ServiceUnit{
			Name: fields[0], Load: fields[1], Active: fields[2], Sub: fields[3],
			Description: strings.Join(fields[4:], " "),
		})
	}
	return units
}

func systemdHostResult(result execbatch.Result, list bool) ServiceHostResult {
	host := ServiceHostResult{Result: result, Systemd: "unknown", Units: []ServiceUnit{}}
	if result.Status == execbatch.StatusSuccess {
		host.Systemd = "supported"
		if list {
			host.Units = ParseSystemdUnits(result.Stdout)
		}
		return host
	}
	host.FailureKind = classifyFailure(result)
	if isSystemdUnsupported(result) {
		host.Systemd = "unsupported"
		host.FailureKind = "systemd"
	}
	return host
}

func classifyFailure(result execbatch.Result) string {
	text := strings.ToLower(strings.Join([]string{result.Error, result.Stderr, result.Stdout}, "\n"))
	switch {
	case strings.Contains(text, "sudo") && (strings.Contains(text, "password") || strings.Contains(text, "not found") || strings.Contains(text, "unavailable") || strings.Contains(text, "no tty")):
		return "sudo"
	case isSystemdUnsupported(result):
		return "systemd"
	case result.Status == execbatch.StatusTransportError:
		return "transport"
	case result.Status == execbatch.StatusTimeout:
		return "timeout"
	case result.Status == execbatch.StatusCanceled:
		return "canceled"
	default:
		return "exit"
	}
}

func isSystemdUnsupported(result execbatch.Result) bool {
	text := strings.ToLower(result.Stderr + "\n" + result.Stdout + "\n" + result.Error)
	patterns := []string{
		"systemctl: command not found", "systemctl: not found", "system has not been booted with systemd",
		"failed to connect to bus", "failed to get d-bus connection", "systemd is not running",
	}
	for _, pattern := range patterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}
