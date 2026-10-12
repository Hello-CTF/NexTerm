package agent

import (
	"fmt"
	"strconv"
	"strings"
)

func parseVMStat(output string) (usedPages, pageSize int64, err error) {
	fields := map[string]int64{}
	for _, line := range strings.Split(output, "\n") {
		if _, after, found := strings.Cut(line, "page size of "); found {
			sizeText, _, _ := strings.Cut(after, " ")
			if size, convErr := strconv.ParseInt(sizeText, 10, 64); convErr == nil && size > 0 {
				pageSize = size
			}
			continue
		}
		name, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value := strings.TrimSuffix(strings.TrimSpace(rest), ".")
		parsed, convErr := strconv.ParseInt(value, 10, 64)
		if convErr != nil {
			continue
		}
		fields[strings.TrimSpace(name)] = parsed
	}
	active, activeOK := fields["Pages active"]
	wired, wiredOK := fields["Pages wired down"]
	if !activeOK || !wiredOK {
		return 0, 0, fmt.Errorf("vm_stat output missing active or wired pages")
	}
	return active + wired + fields["Pages occupied by compressor"], pageSize, nil
}

func parseTopCPUPercent(output string) (float64, error) {
	seen := 0
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "CPU usage:") {
			continue
		}
		seen++
		if seen < 2 {
			continue
		}
		for _, segment := range strings.Split(line, ",") {
			value, found := strings.CutSuffix(strings.TrimSpace(segment), "% idle")
			if !found {
				continue
			}
			idle, convErr := strconv.ParseFloat(value, 64)
			if convErr != nil {
				return 0, fmt.Errorf("parse top idle %q: %w", value, convErr)
			}
			pct := 100 - idle
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			return pct, nil
		}
		return 0, fmt.Errorf("top CPU usage line missing idle field %q", line)
	}
	return 0, fmt.Errorf("top output missing second CPU usage line")
}
