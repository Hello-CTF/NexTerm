package execbatch

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const (
	healthSectionMarker = "__NEXTERM_HEALTH_V1_SECTION__"
	healthEndMarker     = "__NEXTERM_HEALTH_V1_END__"
)

var (
	uptimePattern       = regexp.MustCompile(`\bup\s+(.+?)(?:,\s*\d+\s+users?\b.*|,\s*load averages?:.*|$)`)
	uptimeClockPattern  = regexp.MustCompile(`^(\d+):(\d{2})(?::(\d{2}))?$`)
	uptimeUnitPattern   = regexp.MustCompile(`(\d+)\s*(weeks?|days?|hours?|hrs?|minutes?|mins?|seconds?|secs?|w|d|h|m|s)\b`)
	dockerDFSLine       = regexp.MustCompile(`^(Images|Containers|Local Volumes|Build Cache)\s+(\d+)\s+(\d+)\s+(\S+)\s+(\S+)(?:\s+\([^)]*\))?\s*$`)
	dockerDFSizePattern = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)([kmgtpe]?i?B)$`)
)

type healthMetrics struct {
	uptimeSeconds          *int64
	load1                  *float64
	load5                  *float64
	load15                 *float64
	diskUsedPercent        *float64
	memoryUsedPercent      *float64
	dockerReclaimableBytes *int64
	errors                 []ExecBatchHealthMetricError
}

type healthSection struct {
	lines  []string
	status int
	ended  bool
}

func parseHealthOutput(output string) healthMetrics {
	sections := splitHealthSections(output)
	metrics := healthMetrics{}
	if section, ok := successfulHealthSection(sections, "uptime", &metrics); ok {
		value, err := parseUptimeSeconds(strings.Join(section.lines, "\n"))
		if err != nil {
			addHealthMetricError(&metrics, "uptime", err.Error())
		} else {
			metrics.uptimeSeconds = &value
		}
	}
	if section, ok := successfulHealthSection(sections, "load", &metrics); ok {
		values, err := parseLoadAverage(strings.Join(section.lines, "\n"))
		if err != nil {
			addHealthMetricError(&metrics, "load", err.Error())
		} else {
			metrics.load1 = &values[0]
			metrics.load5 = &values[1]
			metrics.load15 = &values[2]
		}
	}
	if section, ok := successfulHealthSection(sections, "disk", &metrics); ok {
		value, err := parseDiskUsedPercent(strings.Join(section.lines, "\n"))
		if err != nil {
			addHealthMetricError(&metrics, "disk", err.Error())
		} else {
			metrics.diskUsedPercent = &value
		}
	}
	if section, ok := successfulHealthSection(sections, "memory", &metrics); ok {
		value, err := parseMemoryUsedPercent(strings.Join(section.lines, "\n"))
		if err != nil {
			addHealthMetricError(&metrics, "memory", err.Error())
		} else {
			metrics.memoryUsedPercent = &value
		}
	}
	if section, ok := successfulHealthSection(sections, "docker", &metrics); ok {
		value, err := parseDockerReclaimable(strings.Join(section.lines, "\n"))
		if err != nil {
			addHealthMetricError(&metrics, "docker", err.Error())
		} else {
			metrics.dockerReclaimableBytes = &value
		}
	}
	return metrics
}

func splitHealthSections(output string) map[string]*healthSection {
	sections := make(map[string]*healthSection)
	current := ""
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if name, found := strings.CutPrefix(trimmed, healthSectionMarker); found {
			current = name
			if _, exists := sections[current]; !exists {
				sections[current] = &healthSection{}
			}
			continue
		}
		if value, found := strings.CutPrefix(trimmed, healthEndMarker); found {
			name, statusText, valid := strings.Cut(value, ":")
			status, err := strconv.Atoi(statusText)
			if valid && err == nil {
				section := sections[name]
				if section == nil {
					section = &healthSection{}
					sections[name] = section
				}
				section.status = status
				section.ended = true
			}
			current = ""
			continue
		}
		if current != "" {
			sections[current].lines = append(sections[current].lines, line)
		}
	}
	return sections
}

func successfulHealthSection(sections map[string]*healthSection, name string, metrics *healthMetrics) (*healthSection, bool) {
	section := sections[name]
	if section == nil || !section.ended {
		addHealthMetricError(metrics, name, "probe section is missing or incomplete")
		return nil, false
	}
	if section.status != 0 {
		detail := strings.TrimSpace(strings.Join(section.lines, "\n"))
		if len([]rune(detail)) > 300 {
			detail = string([]rune(detail)[:300]) + "…"
		}
		if detail == "" {
			detail = "command returned no diagnostic output"
		}
		addHealthMetricError(metrics, name, fmt.Sprintf("command exited %d: %s", section.status, detail))
		return nil, false
	}
	return section, true
}

func addHealthMetricError(metrics *healthMetrics, metric, message string) {
	metrics.errors = append(metrics.errors, ExecBatchHealthMetricError{Metric: metric, Error: message})
}

func parseUptimeSeconds(output string) (int64, error) {
	match := uptimePattern.FindStringSubmatch(strings.TrimSpace(output))
	if match == nil {
		return 0, fmt.Errorf("unsupported uptime output %q", strings.TrimSpace(output))
	}
	var total int64
	parts := strings.Split(match[1], ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if clock := uptimeClockPattern.FindStringSubmatch(part); clock != nil {
			hours, _ := strconv.ParseInt(clock[1], 10, 64)
			minutes, _ := strconv.ParseInt(clock[2], 10, 64)
			seconds := int64(0)
			if clock[3] != "" {
				seconds, _ = strconv.ParseInt(clock[3], 10, 64)
			}
			if minutes > 59 || seconds > 59 || hours > (math.MaxInt64-total)/3600 {
				return 0, fmt.Errorf("invalid uptime duration %q", part)
			}
			total += hours*3600 + minutes*60 + seconds
			continue
		}
		matches := uptimeUnitPattern.FindAllStringSubmatchIndex(part, -1)
		if len(matches) == 0 {
			return 0, fmt.Errorf("unsupported uptime duration %q", part)
		}
		position := 0
		for _, match := range matches {
			if strings.TrimSpace(part[position:match[0]]) != "" {
				return 0, fmt.Errorf("unsupported uptime duration %q", part)
			}
			value, err := strconv.ParseInt(part[match[2]:match[3]], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid uptime duration %q: %w", part, err)
			}
			unit := part[match[4]:match[5]]
			multiplier, ok := uptimeUnitSeconds(unit)
			if !ok || value > (math.MaxInt64-total)/multiplier {
				return 0, fmt.Errorf("invalid uptime duration %q", part)
			}
			total += value * multiplier
			position = match[1]
		}
		if strings.TrimSpace(part[position:]) != "" {
			return 0, fmt.Errorf("unsupported uptime duration %q", part)
		}
	}
	return total, nil
}

func uptimeUnitSeconds(unit string) (int64, bool) {
	switch unit {
	case "week", "weeks", "w":
		return 7 * 24 * 3600, true
	case "day", "days", "d":
		return 24 * 3600, true
	case "hour", "hours", "hr", "hrs", "h":
		return 3600, true
	case "minute", "minutes", "min", "mins", "m":
		return 60, true
	case "second", "seconds", "sec", "secs", "s":
		return 1, true
	default:
		return 0, false
	}
}

func parseLoadAverage(output string) ([3]float64, error) {
	fields := strings.Fields(output)
	if len(fields) < 3 {
		return [3]float64{}, fmt.Errorf("unsupported /proc/loadavg output %q", strings.TrimSpace(output))
	}
	var values [3]float64
	for index := range values {
		value, err := strconv.ParseFloat(fields[index], 64)
		if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return [3]float64{}, fmt.Errorf("invalid load average %q", fields[index])
		}
		values[index] = value
	}
	return values, nil
}

func parseDiskUsedPercent(output string) (float64, error) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[len(fields)-1] != "/" {
			continue
		}
		capacity := fields[len(fields)-2]
		if !strings.HasSuffix(capacity, "%") {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSuffix(capacity, "%"), 64)
		if err == nil && value >= 0 && value <= 100 {
			return value, nil
		}
	}
	return 0, fmt.Errorf("root filesystem capacity not found in df output")
}

func parseMemoryUsedPercent(output string) (float64, error) {
	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		return 0, fmt.Errorf("free output is empty")
	}
	header := strings.Fields(lines[0])
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "Mem:" {
			continue
		}
		values := fields[1:]
		if len(header) != len(values) {
			return 0, fmt.Errorf("free header and Mem row have different column counts")
		}
		columns := make(map[string]int64, len(values))
		for index, name := range header {
			value, err := strconv.ParseInt(values[index], 10, 64)
			if err != nil || value < 0 {
				return 0, fmt.Errorf("invalid free column %s=%q", name, values[index])
			}
			columns[name] = value
		}
		total := columns["total"]
		if total <= 0 {
			return 0, fmt.Errorf("free reported a zero memory total")
		}
		used := columns["used"]
		if available, ok := columns["available"]; ok {
			used = total - available
		} else {
			used -= columns["buffers"] + columns["cached"]
		}
		if used < 0 || used > total {
			return 0, fmt.Errorf("free reported memory usage outside its total")
		}
		return float64(used) * 100 / float64(total), nil
	}
	return 0, fmt.Errorf("Mem row not found in free output")
}

func parseDockerReclaimable(output string) (int64, error) {
	var total int64
	matched := 0
	for _, line := range strings.Split(output, "\n") {
		match := dockerDFSLine.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		value, err := parseDockerDFSize(match[5])
		if err != nil {
			return 0, fmt.Errorf("parse %s reclaimable size: %w", match[1], err)
		}
		if value > math.MaxInt64-total {
			return 0, fmt.Errorf("docker reclaimable size overflows int64")
		}
		total += value
		matched++
	}
	if matched == 0 {
		return 0, fmt.Errorf("docker system df summary not found")
	}
	return total, nil
}

func parseDockerDFSize(value string) (int64, error) {
	match := dockerDFSizePattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return 0, fmt.Errorf("unsupported docker size %q", value)
	}
	number, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, err
	}
	multipliers := map[string]float64{
		"b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12, "pb": 1e15, "eb": 1e18,
		"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40, "pib": 1 << 50, "eib": 1 << 60,
	}
	multiplier := multipliers[strings.ToLower(match[2])]
	bytes := number * multiplier
	if bytes < 0 || bytes > math.MaxInt64 || math.IsNaN(bytes) || math.IsInf(bytes, 0) {
		return 0, fmt.Errorf("docker size %q is outside int64", value)
	}
	return int64(math.Round(bytes)), nil
}
