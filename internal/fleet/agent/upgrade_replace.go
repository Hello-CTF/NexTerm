package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Files kept next to the agent binary during a replacement. They live beside the binary because both the replace
// script and the restarted agent know that path, and the directory is known to be writable.
const (
	// upgradeBackupSuffix names the previous binary, kept until the new one reports healthy.
	upgradeBackupSuffix = ".nexterm-old"
	// upgradeHealthySuffix names the marker the new agent writes, holding its version, after its first
	// successful control-channel connection.
	upgradeHealthySuffix = ".nexterm-healthy"
	// upgradeFailedSuffix names the record of rolled-back versions: one "<version> <unix seconds> <reason>" line each.
	upgradeFailedSuffix = ".nexterm-failed"
	// upgradeFailureUnhealthy is the reason recorded when the new agent did not report healthy in time.
	upgradeFailureUnhealthy = "unhealthy"
)

// replacePlan holds the parameters of one deferred replacement; the Unix and Windows scripts render the same plan.
type replacePlan struct {
	NewPath, TargetPath string
	// Version is the release being installed; the health marker must hold exactly this value.
	Version string
	// WaitPID is the process the script waits for before swapping; 0 skips the wait.
	WaitPID            int
	WaitTimeoutSeconds int
	// HealthTimeoutSeconds bounds the wait for the health marker after a successful restart.
	HealthTimeoutSeconds int
	// LogPath receives outcome lines; empty disables logging.
	LogPath string
}

func newReplacePlan(newPath, targetPath, version, logPath string, waitPID int) replacePlan {
	return replacePlan{
		NewPath: filepath.Clean(newPath), TargetPath: filepath.Clean(targetPath), Version: version,
		WaitPID: waitPID, WaitTimeoutSeconds: replaceWaitTimeoutSeconds, HealthTimeoutSeconds: replaceHealthTimeoutSeconds,
		LogPath: logPath,
	}
}

func (p replacePlan) BackupPath() string  { return p.TargetPath + upgradeBackupSuffix }
func (p replacePlan) MarkerPath() string  { return p.TargetPath + upgradeHealthySuffix }
func (p replacePlan) FailurePath() string { return p.TargetPath + upgradeFailedSuffix }

// checkStagedVersion runs the staged binary's version command and requires it to report targetVersion, so a binary that
// cannot start on this host (wrong architecture, missing loader, noexec mount) or reports another version is never
// swapped in. A version mismatch is a permanent refusal; a binary that fails to run may be retried.
func checkStagedVersion(ctx context.Context, path, targetVersion string) error {
	runCtx, cancel := context.WithTimeout(ctx, stagedVersionTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, path, "version")
	cmd.WaitDelay = time.Second
	prepareStagedCommand(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedBuffer{buf: &stdout, limit: 4 << 10}
	cmd.Stderr = &limitedBuffer{buf: &stderr, limit: 4 << 10}
	err := cmd.Run()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("新版本程序自检超时：%s version 在 %s 内没有结束，拒绝替换", path, stagedVersionTimeout)
	}
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return fmt.Errorf("新版本程序无法在本机运行（%v: %s），拒绝替换", err, detail)
		}
		return fmt.Errorf("新版本程序无法在本机运行（%v），拒绝替换", err)
	}
	reported, _, _ := strings.Cut(strings.TrimSpace(stdout.String()), "\n")
	reported = strings.TrimPrefix(strings.TrimSpace(reported), "v")
	if reported != targetVersion {
		return fmt.Errorf("%w: 新版本程序自报版本 %q，与目标版本 %s 不符", errUpgradeRefused, reported, targetVersion)
	}
	return nil
}

type limitedBuffer struct {
	buf   *bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

// unixRestartCommand is the command the script runs to (re)start the service after a swap and after a rollback
// (darwin=launchctl kickstart -k, which also replaces an instance that is still running; linux=systemctl --user restart).
// The script records its output and exit code in the upgrade log and never fails on it. A rollback restarts the previous
// binary at most once; it does not loop because the restored agent backs off from the version that failed.
func unixRestartCommand(goos string) string {
	if goos == "darwin" {
		return `launchctl kickstart -k "gui/$(id -u)/` + launchdLabel + `"`
	}
	return "systemctl --user restart " + systemdUnitName
}

// renderReplaceScriptUnix generates the deferred replacement script. A running process cannot overwrite itself, so the
// script waits for WaitPID to exit, then swaps the new binary in with same-directory renames, keeping the previous
// binary as a backup. When restartCmd succeeds it waits up to HealthTimeoutSeconds for the new agent's health marker;
// without it the backup is restored, the failure recorded and restartCmd run again. When restartCmd fails (no service
// installed) nothing can confirm health, so the new binary stays, as before health checks existed. No path is ever
// left without a binary.
func renderReplaceScriptUnix(p replacePlan, restartCmd string) string {
	logPath := p.LogPath
	if logPath == "" {
		logPath = os.DevNull
	}
	target, backup, marker := shQuote(p.TargetPath), shQuote(p.BackupPath()), shQuote(p.MarkerPath())
	version := shQuote(p.Version)
	lines := []string{
		"#!/bin/sh",
		"NX_LOG=" + shQuote(logPath),
		// A failed redirection on a compound command skips the whole command, so probe the log in a subshell (a failed
		// redirect on a special builtin exits the shell) and fall back to /dev/null.
		`( : >>"$NX_LOG" ) 2>/dev/null || NX_LOG=/dev/null`,
		`nx_log() { echo "$(date) $*" >>"$NX_LOG" 2>&1; }`,
		"nx_restart() {",
		"  { " + restartCmd,
		`  } >>"$NX_LOG" 2>&1`,
		"  nx_rc=$?",
		`  nx_log "` + upgradeLogRestartExit + ` $nx_rc"`,
		"  return $nx_rc",
		"}",
		"nx_healthy() { [ \"$(cat " + marker + " 2>/dev/null)\" = " + version + " ]; }",
	}
	if p.WaitPID > 0 {
		lines = append(lines,
			"nx_waited=0",
			"while kill -0 "+strconv.Itoa(p.WaitPID)+" 2>/dev/null && [ \"$nx_waited\" -lt "+strconv.Itoa(p.WaitTimeoutSeconds)+" ]; do sleep 1; nx_waited=$((nx_waited + 1)); done",
		)
	}
	lines = append(lines,
		"rm -f "+marker,
		"mv -f "+target+" "+backup+" 2>/dev/null || true",
		"if mv -f "+shQuote(p.NewPath)+" "+target+"; then",
		"  chmod 0755 "+target,
		`  nx_log "replaced binary"`,
		"  if nx_restart; then",
		"    nx_waited=0",
		"    while ! nx_healthy && [ \"$nx_waited\" -lt "+strconv.Itoa(p.HealthTimeoutSeconds)+" ]; do sleep 1; nx_waited=$((nx_waited + 1)); done",
		"    if nx_healthy; then",
		"      rm -f "+backup+" "+marker,
		`      nx_log "new agent reported healthy"`,
		"    elif [ -f "+backup+" ]; then",
		`      nx_log "new agent did not report a control channel connection within `+strconv.Itoa(p.HealthTimeoutSeconds)+`s; restoring the previous binary"`,
		"      if mv -f "+backup+" "+target+"; then",
		"        echo "+version+" \"$(date +%s)\" "+upgradeFailureUnhealthy+" >>"+shQuote(p.FailurePath())+" 2>/dev/null",
		`        nx_log "rolled back to the previous binary"`,
		"        nx_restart",
		"      else",
		`        nx_log "restoring the previous binary failed; keeping the new binary"`,
		"      fi",
		"    else",
		`      nx_log "new agent did not report healthy and no previous binary exists; keeping the new binary"`,
		"    fi",
		"  else",
		"    rm -f "+backup,
		`    nx_log "restart failed; health check skipped, keeping the new binary"`,
		"  fi",
		"else",
		"  mv -f "+backup+" "+target+" 2>/dev/null || true",
		`  nx_log "swap failed; rolled back to the previous binary"`,
		"fi",
		`rm -f "$0"`,
	)
	return strings.Join(lines, "\n") + "\n"
}

// windowsReplaceScript describes one deferred replace script for Windows.
type windowsReplaceScript struct {
	replacePlan
	// RestartBlock (re)starts the agent after a swap or a rollback (see windowsRestartBlock). It sets
	// $script:nxRestarted to $true only when a new instance was actually started; otherwise the health wait is skipped.
	RestartBlock string
	// StopBlock ends a running instance of the new agent before the rollback restores the previous binary.
	StopBlock string
}

func newWindowsReplaceScript(plan replacePlan, taskName string) windowsReplaceScript {
	return windowsReplaceScript{replacePlan: plan, RestartBlock: windowsRestartBlock(taskName), StopBlock: windowsStopBlock(taskName)}
}

// windowsRestartBlock restarts the scheduled task: the agent exits with ExitOK, which the task's RestartOnFailure
// policy does not restart. The task uses MultipleInstancesPolicy=IgnoreNew, so /Run is silently dropped while the old
// instance is still tracked as running; the block therefore waits (up to ~30s) for the task to leave Running, then
// records /Run's exit code and the resulting state. It sets $script:nxRestarted only when /Run succeeded on a task that
// was not running, the only case in which a new instance exists to report health. Every failure is swallowed so the
// script still cleans up after itself.
func windowsRestartBlock(taskName string) string {
	task := psQuote(taskName)
	lines := []string{
		"function nxTaskState { try { (Get-ScheduledTask -TaskName " + task + " -ErrorAction Stop).State.ToString() } catch { 'Unknown' } }",
		"try {",
		"  $nxDeadline = (Get-Date).AddSeconds(30)",
		"  $nxState = nxTaskState",
		"  while ($nxState -eq 'Running' -and (Get-Date) -lt $nxDeadline) { Start-Sleep -Milliseconds 500; $nxState = nxTaskState }",
		"  nxLog \"task state before /Run: $nxState\"",
		"  $nxOut = (& schtasks.exe /Run /TN " + task + " 2>&1 | Out-String).Trim()",
		"  $nxRunExit = $LASTEXITCODE",
		"  nxLog \"schtasks /Run exit code $($nxRunExit): $nxOut\"",
		"  if ($nxRunExit -eq 0 -and $nxState -ne 'Running') { $script:nxRestarted = $true }",
		"  Start-Sleep -Seconds 1",
		"  nxLog \"task state after /Run: $(nxTaskState)\"",
		"} catch { nxLog \"restart failed: $_\" }",
	}
	return strings.Join(lines, "\n")
}

// windowsStopBlock ends the scheduled task so the new binary is no longer running when the rollback restores the backup.
func windowsStopBlock(taskName string) string {
	return "try { $nxOut = (& schtasks.exe /End /TN " + psQuote(taskName) + " 2>&1 | Out-String).Trim(); nxLog \"schtasks /End exit code $($LASTEXITCODE): $nxOut\"; Start-Sleep -Seconds 2 } catch { nxLog \"stop failed: $_\" }"
}

// windowsScriptBOM makes Windows PowerShell 5.1 read the script as UTF-8 instead of the ANSI code page.
const windowsScriptBOM = "\xEF\xBB\xBF"

// writeReplaceScriptFile writes the script to a new temp file in dir, prefixed with a UTF-8 BOM, and returns its path.
func writeReplaceScriptFile(dir, body string) (string, error) {
	script, err := os.CreateTemp(dir, ".nexterm-replace-*.ps1")
	if err != nil {
		return "", err
	}
	path := script.Name()
	if _, err := script.WriteString(windowsScriptBOM + body); err != nil {
		_ = script.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := script.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// renderReplaceScriptWindows is the windows counterpart of renderReplaceScriptUnix with the same health and rollback rules.
// A running executable can be renamed but not overwritten, so the rollback first ends the task and moves the new binary aside.
func renderReplaceScriptWindows(p windowsReplaceScript) string {
	var b strings.Builder
	b.WriteString("$nxLogPath = " + psQuote(p.LogPath) + "\n")
	b.WriteString("function nxLog($m) { if ($nxLogPath) { try { Add-Content -LiteralPath $nxLogPath -Encoding UTF8 -Value ((Get-Date -Format o) + ' ' + $m) -ErrorAction Stop } catch {} } }\n")
	b.WriteString("function nxRestart {\n" + p.RestartBlock + "\n}\n")
	b.WriteString("function nxStop {\n" + p.StopBlock + "\n}\n")
	if p.WaitPID > 0 {
		b.WriteString("try { Wait-Process -Id " + strconv.Itoa(p.WaitPID) + " -Timeout " + strconv.Itoa(p.WaitTimeoutSeconds) + " -ErrorAction SilentlyContinue } catch {}\n")
	}
	b.WriteString("$nxTarget = " + psQuote(p.TargetPath) + "\n")
	b.WriteString("$nxNew = " + psQuote(p.NewPath) + "\n")
	b.WriteString("$nxBak = " + psQuote(p.BackupPath()) + "\n")
	b.WriteString("$nxMarker = " + psQuote(p.MarkerPath()) + "\n")
	b.WriteString("$nxFailed = " + psQuote(p.FailurePath()) + "\n")
	b.WriteString("$nxVersion = " + psQuote(p.Version) + "\n")
	b.WriteString("function nxHealthy { try { (Get-Content -LiteralPath $nxMarker -Raw -ErrorAction Stop).Trim() -eq $nxVersion } catch { $false } }\n")
	b.WriteString("Remove-Item -LiteralPath $nxMarker -Force -ErrorAction SilentlyContinue\n")
	b.WriteString("try { Move-Item -LiteralPath $nxTarget -Destination $nxBak -Force -ErrorAction Stop } catch {}\n")
	b.WriteString("$nxSwapped = $false\n")
	b.WriteString("try {\n")
	b.WriteString("  Move-Item -LiteralPath $nxNew -Destination $nxTarget -Force -ErrorAction Stop\n")
	b.WriteString("  $nxSwapped = $true\n")
	b.WriteString("} catch {\n")
	b.WriteString("  try { Move-Item -LiteralPath $nxBak -Destination $nxTarget -Force -ErrorAction Stop } catch {}\n")
	b.WriteString("  nxLog 'swap failed; rolled back to the previous binary'\n")
	b.WriteString("}\n")
	b.WriteString("if ($nxSwapped) {\n")
	b.WriteString("  nxLog 'replaced binary'\n")
	b.WriteString("  $script:nxRestarted = $false\n")
	b.WriteString("  nxRestart\n")
	b.WriteString("  if (-not $script:nxRestarted) {\n")
	b.WriteString("    Remove-Item -LiteralPath $nxBak -Force -ErrorAction SilentlyContinue\n")
	b.WriteString("    nxLog 'restart not confirmed; health check skipped, keeping the new binary'\n")
	b.WriteString("  } else {\n")
	b.WriteString("    $nxDeadline = (Get-Date).AddSeconds(" + strconv.Itoa(p.HealthTimeoutSeconds) + ")\n")
	b.WriteString("    while (-not (nxHealthy) -and (Get-Date) -lt $nxDeadline) { Start-Sleep -Seconds 1 }\n")
	b.WriteString("    if (nxHealthy) {\n")
	b.WriteString("      Remove-Item -LiteralPath $nxBak -Force -ErrorAction SilentlyContinue\n")
	b.WriteString("      Remove-Item -LiteralPath $nxMarker -Force -ErrorAction SilentlyContinue\n")
	b.WriteString("      nxLog 'new agent reported healthy'\n")
	b.WriteString("    } elseif (Test-Path -LiteralPath $nxBak) {\n")
	b.WriteString("      nxLog 'new agent did not report a control channel connection within " + strconv.Itoa(p.HealthTimeoutSeconds) + "s; restoring the previous binary'\n")
	b.WriteString("      nxStop\n")
	b.WriteString("      $nxBad = $nxTarget + '.nexterm-bad'\n")
	b.WriteString("      try {\n")
	b.WriteString("        Move-Item -LiteralPath $nxTarget -Destination $nxBad -Force -ErrorAction Stop\n")
	b.WriteString("        Move-Item -LiteralPath $nxBak -Destination $nxTarget -Force -ErrorAction Stop\n")
	b.WriteString("        Remove-Item -LiteralPath $nxBad -Force -ErrorAction SilentlyContinue\n")
	b.WriteString("        try { Add-Content -LiteralPath $nxFailed -Encoding ASCII -Value ($nxVersion + ' ' + [DateTimeOffset]::UtcNow.ToUnixTimeSeconds() + ' " + upgradeFailureUnhealthy + "') -ErrorAction Stop } catch {}\n")
	b.WriteString("        nxLog 'rolled back to the previous binary'\n")
	b.WriteString("      } catch {\n")
	b.WriteString("        if (-not (Test-Path -LiteralPath $nxTarget)) { try { Move-Item -LiteralPath $nxBad -Destination $nxTarget -Force -ErrorAction Stop } catch {} }\n")
	b.WriteString("        nxLog \"restoring the previous binary failed: $_\"\n")
	b.WriteString("      }\n")
	b.WriteString("      nxRestart\n")
	b.WriteString("    } else {\n")
	b.WriteString("      nxLog 'new agent did not report healthy and no previous binary exists; keeping the new binary'\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	b.WriteString("Remove-Item -LiteralPath $PSCommandPath -Force -ErrorAction SilentlyContinue\n")
	return b.String()
}
