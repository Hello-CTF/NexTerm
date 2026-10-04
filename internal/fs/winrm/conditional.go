package winrm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/fs/conditional"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

const (
	winrmOutcomeExists    = "@{s='mismatch';r='file already exists';e=$true;l=0;h=''}"
	winrmOutcomeCommitted = "@{s='committed'}"
	winrmOutcomeError     = "@{s='error';c=$__nextermCommitted;m=$_.Exception.Message}"
	winrmTempCleanup      = "if (-not $__nextermCommitted -and (Test-Path -LiteralPath $__nextermTemp)) { Remove-Item -LiteralPath $__nextermTemp -Force -ErrorAction SilentlyContinue }"
)

type conditionalOutcome struct {
	Status    string `json:"s"`
	Reason    string `json:"r"`
	Exists    bool   `json:"e"`
	Size      int64  `json:"l"`
	SHA256    string `json:"h"`
	Message   string `json:"m"`
	Committed *bool  `json:"c"`
}

func (f *FileSystem) WriteFileVersion(ctx context.Context, path string, data []byte, backup bool, expected conditional.Expectation) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	if len(data) > MaxWriteBytes {
		return fmt.Errorf("WinRM write limit is %d bytes, got %d", MaxWriteBytes, len(data))
	}
	if expected.Exists {
		return fmt.Errorf("WinRM conditional replace %s: %w: File.Replace cannot be conditioned on the verified identity and content against non-cooperating writers", path, base.ErrUnsupported)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result, err := f.executor.Exec(ctx, conditionalCreateScript(path, data), base.ExecOptions{
		Limits: base.OutputLimits{Stdout: -1, Stderr: -1},
	})
	if err != nil {
		return indeterminateCommit(expected, data, err)
	}
	if result.ExitCode == nil {
		return indeterminateCommit(expected, data, base.ErrExitStatusMissing)
	}
	output := strings.TrimSpace(result.Stdout)
	if *result.ExitCode != 0 {
		message := strings.TrimSpace(result.Stderr)
		if message == "" {
			message = fmt.Sprintf("WinRM conditional write exited with status %d", *result.ExitCode)
		}
		return indeterminateCommit(expected, data, fmt.Errorf("WinRM conditional write: %s", message))
	}
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
	case "error":
		if outcome.Committed == nil {
			return indeterminateCommit(expected, data, fmt.Errorf("WinRM conditional write error outcome lacks an explicit committed flag: %s", outcome.Message))
		}
		if *outcome.Committed {
			return indeterminateCommit(expected, data, fmt.Errorf("WinRM conditional write failed after the commit: %s", outcome.Message))
		}
		return fmt.Errorf("WinRM conditional write: %s", outcome.Message)
	default:
		return indeterminateCommit(expected, data, fmt.Errorf("unexpected WinRM conditional write response %q", output))
	}
}

func indeterminateCommit(expected conditional.Expectation, data []byte, cause error) error {
	return &conditional.IndeterminateError{Expected: expected, New: conditional.VersionOf(data), Cause: cause}
}

func conditionalCreateScript(path string, data []byte) string {
	return "$ErrorActionPreference='Stop'; $__nextermCommitted=$false; " +
		"try { " +
		resolvePath(path) +
		"$__nextermData=[Convert]::FromBase64String('" + base64.StdEncoding.EncodeToString(data) + "'); " +
		"$__nextermTemp=$__nextermPath+'.nexterm-tmp-'+[Guid]::NewGuid().ToString('N'); " +
		"if (Test-Path -LiteralPath $__nextermPath) { $__nextermOutcome=" + winrmOutcomeExists + " } else { " +
		"try { " +
		"[IO.File]::WriteAllBytes($__nextermTemp,$__nextermData); " +
		"if (Test-Path -LiteralPath $__nextermPath) { $__nextermOutcome=" + winrmOutcomeExists + " } else { " +
		"try { " +
		"[IO.File]::Move($__nextermTemp,$__nextermPath); " +
		"$__nextermCommitted=$true; " +
		"$__nextermOutcome=" + winrmOutcomeCommitted + " " +
		"} catch [IO.IOException] { " +
		"if (Test-Path -LiteralPath $__nextermPath) { $__nextermOutcome=" + winrmOutcomeExists + " } else { throw } " +
		"} " +
		"} " +
		"} finally { " + winrmTempCleanup + " } " +
		"} " +
		"} catch { $__nextermOutcome=" + winrmOutcomeError + " }; " +
		"ConvertTo-Json -InputObject $__nextermOutcome -Compress"
}

var _ conditional.Writer = (*FileSystem)(nil)
