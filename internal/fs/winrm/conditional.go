package winrm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/fs/conditional"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

const (
	winrmOutcomeMissing   = "@{s='mismatch';r='file is missing';e=$false;l=0;h=''}"
	winrmOutcomeExists    = "@{s='mismatch';r='file already exists';e=$true;l=0;h=''}"
	winrmOutcomeNotReg    = "@{s='mismatch';r='target is not a regular file';e=$true;l=0;h=''}"
	winrmOutcomeCommitted = "@{s='committed'}"
	winrmTempCleanup      = "if (-not $__nextermCommitted -and (Test-Path -LiteralPath $__nextermTemp)) { Remove-Item -LiteralPath $__nextermTemp -Force -ErrorAction SilentlyContinue }"
	winrmShareCommit      = "[IO.FileShare]::ReadWrite -bor [IO.FileShare]::Delete"
)

type conditionalOutcome struct {
	Status string `json:"s"`
	Reason string `json:"r"`
	Exists bool   `json:"e"`
	Size   int64  `json:"l"`
	SHA256 string `json:"h"`
}

// WriteFileVersion implements conditional.Writer. The whole compare-and-write
// runs as a single remote script, so there is no client-side window between
// verification and mutation. For a replacement the target is opened with
// ReadWrite access and then byte-range locked: Windows range locks are
// mandatory, so other processes cannot write the verified content even if
// they already hold a handle, and the lock is not released until after
// File.Replace. The locked content is hashed before and after staging, the
// path's current content is hashed once more through a second stream
// immediately before the commit, and the backup is copied from the locked,
// verified stream. A conditional create commits through File.Move, which
// refuses to overwrite a path that appeared meanwhile. Paths reach the
// script only as single-quoted literals produced by quoteLiteral.
//
// A non-zero script exit is a determinate failure: the script threw, nothing
// runs after the atomic commit, and a failed File.Replace/File.Move leaves
// the target untouched. A transport error, a cancellation after dispatch, a
// missing exit status, or an unreadable/missing success outcome instead
// yields *conditional.IndeterminateError, because the remote script may have
// committed already; the call is never retried automatically.
func (f *FileSystem) WriteFileVersion(ctx context.Context, path string, data []byte, backup bool, expected conditional.Expectation) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	if len(data) > MaxWriteBytes {
		return fmt.Errorf("WinRM write limit is %d bytes, got %d", MaxWriteBytes, len(data))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result, err := f.executor.Exec(ctx, conditionalWriteScript(path, data, backup, expected), base.ExecOptions{
		Limits: base.OutputLimits{Stdout: -1, Stderr: -1},
	})
	if err != nil {
		return indeterminateCommit(expected, data, err)
	}
	if result.ExitCode == nil {
		return indeterminateCommit(expected, data, base.ErrExitStatusMissing)
	}
	if *result.ExitCode != 0 {
		message := strings.TrimSpace(result.Stderr)
		if message == "" {
			message = fmt.Sprintf("WinRM conditional write exited with status %d", *result.ExitCode)
		}
		return fmt.Errorf("WinRM conditional write: %s", message)
	}
	output := strings.TrimSpace(result.Stdout)
	var outcome conditionalOutcome
	if err := json.Unmarshal([]byte(output), &outcome); err != nil {
		return indeterminateCommit(expected, data, fmt.Errorf("parse WinRM conditional write response %q: %w", output, err))
	}
	switch outcome.Status {
	case "committed":
		return nil
	case "mismatch":
		return &conditional.MismatchError{
			Expected: expected,
			Actual:   conditional.Version{Exists: outcome.Exists, Size: outcome.Size, SHA256: outcome.SHA256},
			Reason:   outcome.Reason,
		}
	default:
		return indeterminateCommit(expected, data, fmt.Errorf("unexpected WinRM conditional write response %q", output))
	}
}

func indeterminateCommit(expected conditional.Expectation, data []byte, cause error) error {
	return &conditional.IndeterminateError{Expected: expected, New: conditional.VersionOf(data), Cause: cause}
}

func conditionalWriteScript(path string, data []byte, backup bool, expected conditional.Expectation) string {
	prelude := "$ErrorActionPreference='Stop'; " + resolvePath(path) +
		"$__nextermData=[Convert]::FromBase64String('" + base64.StdEncoding.EncodeToString(data) + "'); " +
		"$__nextermTemp=$__nextermPath+'.nexterm-tmp-'+[Guid]::NewGuid().ToString('N'); "
	if expected.Exists {
		return prelude + conditionalReplaceScript(backup, expected)
	}
	return prelude + conditionalCreateScript()
}

func conditionalReplaceScript(backup bool, expected conditional.Expectation) string {
	verify := func(stream string, rewind bool) string {
		block := "$__nextermLength=" + stream + ".Length; " +
			"if ($__nextermLength -ne " + strconv.FormatInt(expected.Size, 10) + ") { return @{s='mismatch';r='size differs';e=$true;l=$__nextermLength;h=''} }; " +
			"$__nextermSHA=[Security.Cryptography.SHA256]::Create(); " +
			"$__nextermActual=[BitConverter]::ToString($__nextermSHA.ComputeHash(" + stream + ")).Replace('-','').ToLowerInvariant(); " +
			"if ($__nextermActual -ne " + quoteLiteral(expected.SHA256) + ") { return @{s='mismatch';r='content digest differs';e=$true;l=$__nextermLength;h=$__nextermActual} }; "
		if rewind {
			block = stream + ".Position=0; " + block
		}
		return block
	}
	backupClause := ""
	if backup {
		backupClause = "$__nextermStream.Position=0; " +
			"$__nextermBackup=[IO.File]::Open($__nextermPath+'" + BackupSuffix + "',[IO.FileMode]::Create,[IO.FileAccess]::Write,[IO.FileShare]::None); " +
			"try { $__nextermStream.CopyTo($__nextermBackup) } finally { $__nextermBackup.Dispose() }; "
	}
	return "$__nextermOutcome = & { " +
		"if (-not (Test-Path -LiteralPath $__nextermPath)) { return " + winrmOutcomeMissing + " }; " +
		"$__nextermItem=Get-Item -LiteralPath $__nextermPath -Force; " +
		"if ($__nextermItem.PSIsContainer -or ($__nextermItem.Attributes -band [IO.FileAttributes]::ReparsePoint)) { return " + winrmOutcomeNotReg + " }; " +
		"$__nextermCommitted=$false; " +
		"try { " +
		"try { $__nextermStream=[IO.File]::Open($__nextermPath,[IO.FileMode]::Open,[IO.FileAccess]::ReadWrite," + winrmShareCommit + ") } catch [IO.FileNotFoundException] { return " + winrmOutcomeMissing + " } catch [IO.DirectoryNotFoundException] { return " + winrmOutcomeMissing + " }; " +
		"try { " +
		"$__nextermStream.Lock(0,[long]::MaxValue); " +
		verify("$__nextermStream", false) +
		"[IO.File]::WriteAllBytes($__nextermTemp,$__nextermData); " +
		verify("$__nextermStream", true) +
		"try { $__nextermPathStream=[IO.File]::Open($__nextermPath,[IO.FileMode]::Open,[IO.FileAccess]::Read," + winrmShareCommit + ") } catch [IO.FileNotFoundException] { return " + winrmOutcomeMissing + " }; " +
		"try { " + verify("$__nextermPathStream", false) + "} finally { $__nextermPathStream.Dispose() }; " +
		backupClause +
		"try { [IO.File]::Replace($__nextermTemp,$__nextermPath,$null) } catch [IO.FileNotFoundException] { return " + winrmOutcomeMissing + " }; " +
		"} finally { $__nextermStream.Dispose() }; " +
		"$__nextermCommitted=$true; " +
		"return " + winrmOutcomeCommitted + " " +
		"} finally { " + winrmTempCleanup + " } " +
		"}; ConvertTo-Json -InputObject $__nextermOutcome -Compress"
}

func conditionalCreateScript() string {
	return "$__nextermOutcome = & { " +
		"if (Test-Path -LiteralPath $__nextermPath) { return " + winrmOutcomeExists + " }; " +
		"$__nextermCommitted=$false; " +
		"try { " +
		"[IO.File]::WriteAllBytes($__nextermTemp,$__nextermData); " +
		"if (Test-Path -LiteralPath $__nextermPath) { return " + winrmOutcomeExists + " }; " +
		"try { [IO.File]::Move($__nextermTemp,$__nextermPath) } catch [IO.IOException] { if (Test-Path -LiteralPath $__nextermPath) { return " + winrmOutcomeExists + " }; throw }; " +
		"$__nextermCommitted=$true; " +
		"return " + winrmOutcomeCommitted + " " +
		"} finally { " + winrmTempCleanup + " } " +
		"}; ConvertTo-Json -InputObject $__nextermOutcome -Compress"
}

var _ conditional.Writer = (*FileSystem)(nil)
