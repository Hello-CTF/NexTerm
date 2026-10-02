package winrm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ProbiusOfficial/NexTerm/internal/fs/conditional"
)

const (
	winrmOutcomeMissing   = "@{s='mismatch';r='file is missing';e=$false;l=0;h=''}"
	winrmOutcomeExists    = "@{s='mismatch';r='file already exists';e=$true;l=0;h=''}"
	winrmOutcomeNotReg    = "@{s='mismatch';r='target is not a regular file';e=$true;l=0;h=''}"
	winrmOutcomeCommitted = "@{s='committed'}"
	winrmTempCleanup      = "if (-not $__nextermCommitted -and (Test-Path -LiteralPath $__nextermTemp)) { Remove-Item -LiteralPath $__nextermTemp -Force -ErrorAction SilentlyContinue }"
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
// verification and mutation: the target is opened with a share mode that the
// kernel enforces against other writers, its size and SHA-256 are verified,
// the replacement is staged beside it, the still-locked content is verified
// again, the optional backup is copied from the locked file, and the commit
// is an atomic File.Replace. A conditional create commits through File.Move,
// which refuses to overwrite a path that appeared meanwhile. Paths reach the
// script only as single-quoted literals produced by quoteLiteral, and the
// script reports a structured outcome instead of an ambiguous exit status.
func (f *FileSystem) WriteFileVersion(ctx context.Context, path string, data []byte, backup bool, expected conditional.Expectation) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	if len(data) > MaxWriteBytes {
		return fmt.Errorf("WinRM write limit is %d bytes, got %d", MaxWriteBytes, len(data))
	}
	output, err := f.execute(ctx, conditionalWriteScript(path, data, backup, expected))
	if err != nil {
		return err
	}
	var outcome conditionalOutcome
	if err := json.Unmarshal([]byte(output), &outcome); err != nil {
		return fmt.Errorf("parse WinRM conditional write response: %w", err)
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
		return fmt.Errorf("unexpected WinRM conditional write response %q", output)
	}
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
	verify := func(rewind bool) string {
		block := "$__nextermLength=$__nextermStream.Length; " +
			"if ($__nextermLength -ne " + strconv.FormatInt(expected.Size, 10) + ") { return @{s='mismatch';r='size differs';e=$true;l=$__nextermLength;h=''} }; " +
			"$__nextermSHA=[Security.Cryptography.SHA256]::Create(); " +
			"$__nextermActual=[BitConverter]::ToString($__nextermSHA.ComputeHash($__nextermStream)).Replace('-','').ToLowerInvariant(); " +
			"if ($__nextermActual -ne " + quoteLiteral(expected.SHA256) + ") { return @{s='mismatch';r='content digest differs';e=$true;l=$__nextermLength;h=$__nextermActual} }; "
		if rewind {
			block = "$__nextermStream.Position=0; " + block
		}
		return block
	}
	backupClause := ""
	if backup {
		backupClause = "[IO.File]::Copy($__nextermPath,$__nextermPath+'" + BackupSuffix + "',$true); "
	}
	return "$__nextermOutcome = & { " +
		"if (-not (Test-Path -LiteralPath $__nextermPath)) { return " + winrmOutcomeMissing + " }; " +
		"$__nextermItem=Get-Item -LiteralPath $__nextermPath -Force; " +
		"if ($__nextermItem.PSIsContainer -or ($__nextermItem.Attributes -band [IO.FileAttributes]::ReparsePoint)) { return " + winrmOutcomeNotReg + " }; " +
		"$__nextermCommitted=$false; " +
		"try { " +
		"try { $__nextermStream=[IO.File]::Open($__nextermPath,[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::Read) } catch [IO.FileNotFoundException] { return " + winrmOutcomeMissing + " } catch [IO.DirectoryNotFoundException] { return " + winrmOutcomeMissing + " }; " +
		"try { " +
		verify(false) +
		"[IO.File]::WriteAllBytes($__nextermTemp,$__nextermData); " +
		verify(true) +
		backupClause +
		"} finally { $__nextermStream.Dispose() }; " +
		"try { [IO.File]::Replace($__nextermTemp,$__nextermPath,$null) } catch [IO.FileNotFoundException] { return " + winrmOutcomeMissing + " }; " +
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
