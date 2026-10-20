package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	maxWaitTabs  = 16
	maxWaitLines = 100_000
)

// waitTarget is the per-tab scan state of an anchored multi-tab wait_for
// call. The anchor is the exact output sequence the wait started from, so
// already-visible content can never false-match and the reported anchors let
// a follow-up read resume without repeating or skipping output. Pattern and
// line conditions are tracked separately and combine as AND.
type waitTarget struct {
	tabID          string
	fromSeq        uint64
	scanPos        uint64
	carry          []byte
	newLines       int
	patternMatched bool
	linesMatched   bool
	matched        bool
	latest         uint64
	idleMS         int64
	cols           int
	rows           int
	note           string
}

// waitForTabs waits across multiple tabs for a pattern, a new-line count,
// or both (AND), under any/all semantics. A single tab honors since_seq as
// its anchor; every other tab is anchored at the latest output sequence the
// wait observed at start.
func (r *Registry) waitForTabs(waitContext context.Context, jobID string, scope Scope, tabIDs []string, regex *regexp.Regexp, pattern string, lines int, quietMS int64, mode string, sinceSeq uint64) Output {
	if len(tabIDs) > maxWaitTabs {
		return Fail(invalid("tab_ids 最多 %d 个标签页", maxWaitTabs))
	}
	if sinceSeq > 0 && len(tabIDs) > 1 {
		return Fail(invalid("多标签等待不能使用单一 since_seq 锚点；每个标签自动以等待开始时的最新序号为锚点"))
	}
	if quietMS > 0 {
		if _, ok := r.deps.Terminal.(WaitSnapshotTerminal); !ok {
			return Fail(errors.New("该终端不支持同步安静快照"))
		}
	}
	targets := make([]*waitTarget, 0, len(tabIDs))
	seen := make(map[string]bool, len(tabIDs))
	for _, requested := range tabIDs {
		tabID, err := r.resolveTab(waitContext, jobID, scope, requested)
		if err != nil {
			return Fail(err)
		}
		if seen[tabID] {
			continue
		}
		seen[tabID] = true
		targets = append(targets, &waitTarget{tabID: tabID})
	}
	for _, target := range targets {
		screen, err := r.waitSnapshot(waitContext, target.tabID, quietMS > 0)
		if err != nil {
			return Fail(err)
		}
		target.cols, target.rows, target.idleMS = screen.Cols, screen.Rows, screen.IdleMS
		if sinceSeq > 0 {
			target.fromSeq = sinceSeq
		} else {
			_, _, latest, err := r.outputSince(waitContext, target.tabID, 0, 1)
			if err != nil {
				return Fail(err)
			}
			target.fromSeq = latest
		}
		target.scanPos = target.fromSeq
		target.latest = target.fromSeq
	}
	ticker := time.NewTicker(r.deps.pollInterval())
	defer ticker.Stop()
	quietOnly := regex == nil && lines == 0
	for {
		satisfied := 0
		for _, target := range targets {
			if target.matched && quietMS == 0 {
				satisfied++
				continue
			}
			r.scanWaitTarget(waitContext, target, regex, lines)
			if quietMS > 0 && (target.matched || quietOnly) {
				if screen, err := r.waitSnapshot(waitContext, target.tabID, true); err == nil {
					target.idleMS = screen.IdleMS
				}
				// A quiet-only wait is satisfied by silence itself.
				if quietOnly {
					target.matched = target.idleMS >= quietMS
				}
			}
			if target.matched && (quietMS == 0 || target.idleMS >= quietMS) {
				satisfied++
			}
		}
		if satisfied > 0 && mode == "any" {
			return r.waitTabsResult(waitContext, targets, mode, true, false)
		}
		if satisfied == len(targets) {
			return r.waitTabsResult(waitContext, targets, mode, true, false)
		}
		select {
		case <-waitContext.Done():
			if waitContext.Err() == context.DeadlineExceeded {
				return r.waitTabsResult(waitContext, targets, mode, false, true)
			}
			return Fail(waitContext.Err())
		case <-ticker.C:
		}
	}
}

// scanWaitTarget advances one tab's scan cursor through the output produced
// after its anchor, counting newly rendered lines and matching the pattern
// against the new output only.
func (r *Registry) scanWaitTarget(waitContext context.Context, target *waitTarget, regex *regexp.Regexp, lines int) {
	data, start, latest, err := r.outputSince(waitContext, target.tabID, target.scanPos, waitScanBytes)
	if err != nil {
		target.note = "输出读取失败: " + err.Error()
		return
	}
	if start > target.scanPos && target.note == "" {
		target.note = "部分输出已被淘汰"
	}
	window := make([]byte, 0, len(target.carry)+len(data))
	window = append(window, target.carry...)
	window = append(window, data...)
	rendered := renderOutputDiff(window, target.cols, target.rows)
	if lines > 0 {
		carried := len(renderOutputDiff(target.carry, target.cols, target.rows))
		if fresh := len(rendered) - carried; fresh > 0 {
			target.newLines += fresh
		}
	}
	if regex != nil && regex.MatchString(strings.Join(rendered, "\n")) {
		target.patternMatched = true
	}
	if lines > 0 && target.newLines >= lines {
		target.linesMatched = true
	}
	// Pattern and line conditions combine as AND: a wait asking for both
	// ends only when both hold.
	target.matched = (regex == nil || target.patternMatched) && (lines <= 0 || target.linesMatched)
	if len(data) > 0 {
		keep := waitScanOverlap
		if keep > len(window) {
			keep = len(window)
		}
		target.carry = append(target.carry[:0], window[len(window)-keep:]...)
		target.scanPos = start + uint64(len(data))
		target.latest = latest
	}
}

// waitTabsResult renders the structured multi-tab outcome and its text
// summary; per-tab anchors and line counts ride in the wait metadata.
func (r *Registry) waitTabsResult(ctx context.Context, targets []*waitTarget, mode string, satisfied, timedOut bool) Output {
	outcome := &WaitOutcome{Mode: mode, Satisfied: satisfied, TimedOut: timedOut, Tabs: make([]WaitTabOutcome, 0, len(targets))}
	matchedIDs := make([]string, 0, len(targets))
	var summary strings.Builder
	for _, target := range targets {
		tab := WaitTabOutcome{
			TabID:    target.tabID,
			Matched:  target.matched,
			IdleMS:   target.idleMS,
			NewLines: target.newLines,
			FromSeq:  target.fromSeq,
			ToSeq:    target.latest,
			Note:     target.note,
		}
		if state, err := r.commandState(ctx, target.tabID); err == nil {
			tab.Running = state.Running
		}
		outcome.Tabs = append(outcome.Tabs, tab)
		if target.matched {
			matchedIDs = append(matchedIDs, target.tabID)
		}
		fmt.Fprintf(&summary, "\n- %s matched=%v new_lines=%d idle=%dms from_seq=%d to_seq=%d",
			target.tabID, target.matched, target.newLines, target.idleMS, target.fromSeq, target.latest)
		if target.note != "" {
			summary.WriteString(" (" + target.note + ")")
		}
	}
	var headline string
	switch {
	case satisfied && mode == "any":
		headline = fmt.Sprintf("标签页 %s 已满足等待条件", strings.Join(matchedIDs, "、"))
	case satisfied:
		headline = fmt.Sprintf("全部 %d 个标签页已满足等待条件", len(targets))
	case timedOut && mode == "any":
		headline = fmt.Sprintf("等待超时，%d/%d 个标签页满足条件", len(matchedIDs), len(targets))
	default:
		headline = fmt.Sprintf("等待超时，仅 %d/%d 个标签页满足条件", len(matchedIDs), len(targets))
	}
	exit := 0
	if !satisfied {
		exit = 1
	}
	return Output{OK: satisfied, Text: headline + summary.String(), ExitCode: exit, Wait: outcome}
}
