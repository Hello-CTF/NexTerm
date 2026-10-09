package winrm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	cwdSentinelHead = "<<NEXTERM_CWD>>"
	cwdSentinelTail = "<<EOF>>"
	encodingPrefix  = "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8\n"
)

type powerShellRunner interface {
	RunPowerShell(context.Context, string, io.Writer, io.Writer) (int, error)
}

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func wrapCommand(cwd, command string) string {
	return "Set-Location -LiteralPath " + quoteLiteral(cwd) + " -ErrorAction Stop\n" +
		command + "\n" +
		"$__nextermOK = $?\n" +
		"$__nextermExit = $LASTEXITCODE\n" +
		"[Console]::WriteLine(\"`n" + cwdSentinelHead + "$((Get-Location).Path)" + cwdSentinelTail + "\")\n" +
		"if ($null -ne $__nextermExit) { exit $__nextermExit }\n" +
		"if (-not $__nextermOK) { exit 1 }\n" +
		"exit 0"
}

func unwrapCWD(stdout string) (string, string, bool) {
	position := strings.LastIndex(stdout, cwdSentinelHead)
	if position < 0 {
		return stdout, "", false
	}
	head := stdout[:position]
	rest := stdout[position+len(cwdSentinelHead):]
	end := strings.Index(rest, cwdSentinelTail)
	if end < 0 {
		return stdout, "", false
	}
	cwd := strings.TrimSpace(rest[:end])
	if cwd == "" {
		return stdout, "", false
	}
	if strings.HasSuffix(head, "\n") {
		head = strings.TrimSuffix(head, "\n")
		head = strings.TrimSuffix(head, "\r")
	}
	tail := rest[end+len(cwdSentinelTail):]
	if strings.HasPrefix(tail, "\r\n") {
		tail = tail[2:]
	} else if strings.HasPrefix(tail, "\n") {
		tail = tail[1:]
	}
	return head + tail, cwd, true
}

func (t *Transport) Exec(ctx context.Context, command string, options base.ExecOptions) (base.ExecResult, error) {
	started := time.Now()
	result := base.ExecResult{}
	if options.Timeout == 0 {
		options.Timeout = DefaultExecTimeout
	}
	if options.Timeout < 0 {
		result.Duration = time.Since(started)
		return result, fmt.Errorf("WinRM exec timeout must not be negative")
	}
	if err := t.check(ctx, options.ExpectedGeneration); err != nil {
		result.Duration = time.Since(started)
		return result, err
	}
	opCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	stopClose := context.AfterFunc(t.closeCtx, cancel)
	defer stopClose()
	select {
	case t.execGate <- struct{}{}:
		defer func() { <-t.execGate }()
	case <-opCtx.Done():
		result.Duration = time.Since(started)
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if t.closeCtx.Err() != nil {
			return result, base.ErrClosed
		}
		return result, opCtx.Err()
	}
	if err := t.check(ctx, options.ExpectedGeneration); err != nil {
		result.Duration = time.Since(started)
		return result, err
	}
	limits := options.Limits.Normalized()
	var stdout, stderr bytes.Buffer
	script := encodingPrefix + wrapCommand(t.CWD(), command)
	exitCode, runErr := t.runner.RunPowerShell(opCtx, script, &stdout, &stderr)
	text, newCWD, found := unwrapCWD(decodeBytes(stdout.Bytes()))
	if found {
		t.SetCWD(newCWD)
	}
	text, stdoutTruncated := limitText(text, limits.Stdout)
	errorText, stderrTruncated := limitText(decodeBytes(stderr.Bytes()), limits.Stderr)
	result = base.ExecResult{
		Stdout:    text,
		Stderr:    errorText,
		Duration:  time.Since(started),
		Truncated: stdoutTruncated || stderrTruncated,
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if opCtx.Err() != nil {
		if t.closeCtx.Err() != nil {
			return result, base.ErrClosed
		}
		return result, opCtx.Err()
	}
	if runErr == nil {
		result.ExitCode = &exitCode
		return result, nil
	}
	return result, fmt.Errorf("WinRM exec: %w", runErr)
}

func limitText(value string, maximum int64) (string, bool) {
	if maximum < 0 || int64(len(value)) <= maximum {
		return value, false
	}
	return strings.ToValidUTF8(value[:int(maximum)], ""), true
}
