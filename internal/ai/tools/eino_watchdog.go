package tools

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// toolWatchdogSlack is added to a terminal tool's own budget, so its
// structured result always wins and the watchdog only fires when a step
// blocked outside every bound the tool enforces itself.
const toolWatchdogSlack = 30 * time.Second

// waitForMaxTimeout is the largest timeout_ms a wait_for call accepts.
const waitForMaxTimeout = 120 * time.Second

// readScreenWatchdog bounds read_screen, which reads in-memory terminal
// state and has no timeout argument.
const readScreenWatchdog = 60 * time.Second

// toolWatchdog returns the hard upper bound for one call, or 0 when the tool
// keeps only its own bounds. It covers the interactive terminal tools, whose
// budget is fully determined by their arguments. Other tools are exempt on
// purpose: exec_commands may legitimately run 20 commands of up to 300 s each,
// and file, Docker, database and terminal_create calls already bound their
// I/O through their context; abandoning one of those mid-effect would report
// a failure while the side effect continues.
func toolWatchdog(input any) time.Duration {
	switch args := input.(type) {
	case SendKeysArgs:
		budget := sendKeysWriteTimeout + toolWatchdogSlack
		if args.Paste && args.Enter {
			budget += min(time.Duration(max(args.SubmitDelayMS, defaultSubmitDelayMS))*time.Millisecond, maxSubmitDelayMS*time.Millisecond)
		}
		if args.WaitMS > 0 {
			budget += min(time.Duration(args.WaitMS)*time.Millisecond, maxSendKeysWaitMS*time.Millisecond)
		}
		return budget
	case WaitForArgs:
		timeout := time.Duration(args.TimeoutMS) * time.Millisecond
		if args.TimeoutMS <= 0 {
			timeout = 10 * time.Second
		}
		return min(timeout, waitForMaxTimeout) + toolWatchdogSlack
	case ReadScreenArgs:
		return readScreenWatchdog
	}
	return 0
}

// runWatched runs one tool call under its watchdog. When the bound expires
// the call is abandoned with a failure result the model sees, so a step that
// blocked outside the tool's own bounds can never hang the run; the
// abandoned call's context is cancelled so cooperative code stops.
func runWatched(ctx context.Context, name string, limit time.Duration, run func(context.Context) (Output, error)) (Output, error) {
	if limit <= 0 {
		return Guarded(ctx, name, func() (Output, error) { return run(ctx) })
	}
	watched, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	output, err := boundedCall(watched, func() (Output, error) {
		return Guarded(watched, name, func() (Output, error) { return run(watched) })
	})
	if err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) && errors.Is(watched.Err(), context.DeadlineExceeded) {
		return Fail(fmt.Errorf("工具 %s 执行超过 %s 仍未返回，已停止等待；操作可能仍在后台进行，结果未知，请先读取状态确认再决定是否重试", name, limit)), nil
	}
	return output, err
}
