package agent

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Server-driven upgrades to a version that was rolled back wait upgradeFailureBackoff after the first failure,
// doubling with each further failure of that version up to upgradeFailureMaxBackoff.
const (
	upgradeFailureBackoff    = time.Hour
	upgradeFailureMaxBackoff = 24 * time.Hour
)

// upgradeFailure is one rolled-back upgrade recorded by the replace script.
type upgradeFailure struct {
	Version string
	At      time.Time
	Reason  string
}

// describe renders the failure for logs and the service state reported to the server.
func (f upgradeFailure) describe() string {
	reason := f.Reason
	if reason == upgradeFailureUnhealthy {
		reason = fmt.Sprintf("新版本启动后 %d 秒内未连上服务端", replaceHealthTimeoutSeconds)
	}
	return fmt.Sprintf("自动升级到 %s 失败（%s），已于 %s 回滚到之前的版本", f.Version, reason, f.At.Local().Format(time.DateTime))
}

func resolvedExecutable(executable string) string {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		return resolved
	}
	return filepath.Clean(executable)
}

// markUpgradeHealthy writes the health marker the replace script waits for. It only writes while a replacement is in
// progress, which the kept backup of the previous binary shows, so a regular start leaves no files behind. It also
// drops the failure records of the now healthy version. It reports whether the marker was written.
func markUpgradeHealthy(executable, version string) (bool, error) {
	target := resolvedExecutable(executable)
	if _, err := os.Stat(target + upgradeBackupSuffix); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	marker := target + upgradeHealthySuffix
	temp, err := os.CreateTemp(filepath.Dir(target), ".nexterm-healthy-*")
	if err != nil {
		return false, err
	}
	_, writeErr := temp.WriteString(version)
	closeErr := temp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(temp.Name())
		return false, err
	}
	if err := os.Rename(temp.Name(), marker); err != nil {
		_ = os.Remove(temp.Name())
		return false, err
	}
	forgetUpgradeFailures(target, version)
	return true, nil
}

// readUpgradeFailures returns the rolled-back upgrades recorded next to executable, oldest first; malformed lines are skipped.
func readUpgradeFailures(executable string) []upgradeFailure {
	content, err := os.ReadFile(resolvedExecutable(executable) + upgradeFailedSuffix)
	if err != nil {
		return nil
	}
	var out []upgradeFailure
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || validateReleaseVersion(fields[0]) != nil {
			continue
		}
		seconds, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		failure := upgradeFailure{Version: fields[0], At: time.Unix(seconds, 0)}
		if len(fields) > 2 {
			failure.Reason = strings.Join(fields[2:], " ")
		}
		out = append(out, failure)
	}
	return out
}

func forgetUpgradeFailures(target, version string) {
	failures := readUpgradeFailures(target)
	var kept []string
	for _, failure := range failures {
		if failure.Version != version {
			kept = append(kept, fmt.Sprintf("%s %d %s", failure.Version, failure.At.Unix(), failure.Reason))
		}
	}
	path := target + upgradeFailedSuffix
	if len(kept) == 0 {
		_ = os.Remove(path)
		return
	}
	if len(kept) != len(failures) {
		_ = os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o600)
	}
}

// latestUpgradeFailure returns the most recent recorded failure.
func latestUpgradeFailure(failures []upgradeFailure) (upgradeFailure, bool) {
	if len(failures) == 0 {
		return upgradeFailure{}, false
	}
	latest := failures[0]
	for _, failure := range failures[1:] {
		if !failure.At.Before(latest.At) {
			latest = failure
		}
	}
	return latest, true
}

// upgradeRetryAfter returns when a server-driven upgrade to version may be tried again after recorded rollbacks, and
// the last failure of that version; ok is false when the version never failed.
func upgradeRetryAfter(failures []upgradeFailure, version string) (time.Time, upgradeFailure, bool) {
	var last upgradeFailure
	count := 0
	for _, failure := range failures {
		if failure.Version != version {
			continue
		}
		count++
		if count == 1 || !failure.At.Before(last.At) {
			last = failure
		}
	}
	if count == 0 {
		return time.Time{}, upgradeFailure{}, false
	}
	delay := upgradeFailureMaxBackoff
	if count <= 5 {
		delay = min(upgradeFailureBackoff<<(count-1), upgradeFailureMaxBackoff)
	}
	return last.At.Add(delay), last, true
}
