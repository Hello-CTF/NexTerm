package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/session"
)

// TerminalHandle is the stable reference a terminal tab is addressed by in
// tool arguments and results. It binds the tab ID to the session ID it was
// issued for, so resolving it later can tell "gone" (tab vanished) apart
// from "replaced" (the ID now belongs to another session) instead of
// silently addressing a different terminal.
type TerminalHandle struct {
	TabID     string
	SessionID string
}

func (h TerminalHandle) String() string {
	return "t/" + h.TabID + "/" + h.SessionID
}

// ParseTerminalHandle decodes the opaque handle form emitted in tool
// results. Plain tab IDs are not handles and are rejected here.
func ParseTerminalHandle(value string) (TerminalHandle, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 3 || parts[0] != "t" || parts[1] == "" || parts[2] == "" {
		return TerminalHandle{}, invalid("终端句柄格式无效: %q", value)
	}
	return TerminalHandle{TabID: parts[1], SessionID: parts[2]}, nil
}

// operationID derives the deterministic identifier of an uncertain mutation
// from the job, call, and tool names. It lets a later call or a human
// reference the exact operation when querying a result; it never implies the
// operation ran exactly once.
func operationID(jobID, callID, tool string) string {
	sum := sha256.Sum256([]byte(jobID + "\x00" + callID + "\x00" + tool))
	return "op-" + hex.EncodeToString(sum[:8])
}

// resolveHandle verifies the handle against the live tab set: the tab must
// still exist (gone only on a verified not-found; transient, forbidden, and
// internal errors propagate as themselves) and must still belong to the
// session the handle was issued for (replaced otherwise). It never falls
// back to addressing a different terminal.
func (r *Registry) resolveHandle(ctx context.Context, handle TerminalHandle) (string, error) {
	terminal, ok := r.deps.Terminal.(TabSessionTerminal)
	if !ok {
		if r.deps.TabSession == nil {
			return "", errors.New("终端服务不支持句柄解析")
		}
		sessionID := r.deps.TabSession(handle.TabID)
		if sessionID == "" {
			return "", ErrTerminalGone
		}
		if sessionID != handle.SessionID {
			return "", ErrTerminalReplaced
		}
		return handle.TabID, nil
	}
	sessionID, err := terminal.TabSession(ctx, handle.TabID)
	if err != nil {
		if errors.Is(err, session.ErrTabNotFound) {
			return "", ErrTerminalGone
		}
		return "", err
	}
	if sessionID == "" {
		return "", ErrTerminalGone
	}
	if sessionID != handle.SessionID {
		return "", ErrTerminalReplaced
	}
	return handle.TabID, nil
}

// ResolveCallTarget reports the exact terminal handle a tool call targets,
// resolved with the same scope and handle rules the execution applies, so
// callers (the agent runner's tool events) can join the call to roster
// metadata without re-deriving the target. Only single-tab terminal tools
// resolve: multi-tab waits, non-terminal tools, and targets that fail
// resolution report ok=false — an unknown target is never guessed.
func (r *Registry) ResolveCallTarget(ctx context.Context, jobID string, scope Scope, call Call) (TerminalHandle, bool) {
	requested := ""
	switch call.Name {
	case "send_keys", "read_screen", "terminal_rename":
		var args struct {
			TabID string `json:"tab_id"`
		}
		if len(call.Args) > 0 && json.Unmarshal(call.Args, &args) != nil {
			return TerminalHandle{}, false
		}
		requested = args.TabID
	case "wait_for":
		var args struct {
			TabID  string   `json:"tab_id"`
			TabIDs []string `json:"tab_ids"`
		}
		if len(call.Args) > 0 && json.Unmarshal(call.Args, &args) != nil {
			return TerminalHandle{}, false
		}
		switch {
		case args.TabID != "" && len(args.TabIDs) > 0:
			return TerminalHandle{}, false
		case len(args.TabIDs) == 1:
			requested = args.TabIDs[0]
		case len(args.TabIDs) > 1:
			// A multi-tab wait has no single honest target handle.
			return TerminalHandle{}, false
		default:
			requested = args.TabID
		}
	default:
		return TerminalHandle{}, false
	}
	tabID, err := r.resolveTab(ctx, jobID, scope, requested)
	if err != nil {
		return TerminalHandle{}, false
	}
	sessionID, err := r.tabSession(ctx, tabID)
	if err != nil || sessionID == "" {
		return TerminalHandle{}, false
	}
	return TerminalHandle{TabID: tabID, SessionID: sessionID}, true
}

const (
	terminalListDefaultLimit = 50
	terminalListMaxLimit     = 200
)

func (r *Registry) terminalList(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		Cursor string `json:"cursor"`
		Limit  int    `json:"limit"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if args.Limit == 0 {
		args.Limit = terminalListDefaultLimit
	}
	if args.Limit < 1 || args.Limit > terminalListMaxLimit {
		return Fail(invalid("limit 必须在 1-%d 之间", terminalListMaxLimit))
	}
	listing, ok := r.deps.Terminal.(ListTabsTerminal)
	if !ok {
		return Fail(errors.New("该终端不支持列出标签页"))
	}
	tabs, err := listing.ListTerminalTabs(ctx)
	if err != nil {
		return Fail(err)
	}
	sort.Slice(tabs, func(i, j int) bool { return tabs[i].TabID < tabs[j].TabID })
	total := len(tabs)
	start := 0
	if args.Cursor != "" {
		start = sort.Search(len(tabs), func(i int) bool { return tabs[i].TabID > args.Cursor })
	}
	page := tabs[start:]
	if len(page) > args.Limit {
		page = page[:args.Limit]
	}
	next := ""
	if start+len(page) < total && len(page) > 0 {
		next = page[len(page)-1].TabID
	}
	var output strings.Builder
	fmt.Fprintf(&output, "共 %d 个终端标签页，本页 %d 个", total, len(page))
	if next != "" {
		fmt.Fprintf(&output, "；下一页 cursor=%s", next)
	}
	for _, tab := range page {
		line := "\n" + tab.Handle
		fmt.Fprintf(&output, "%s kind=%s", line, orUnknown(tab.Kind))
		if tab.AssetID != "" {
			fmt.Fprintf(&output, " asset=%s", tab.AssetID)
		}
		if tab.Title != "" {
			fmt.Fprintf(&output, " title=%q", tab.Title)
		}
		if tab.CWD != "" {
			fmt.Fprintf(&output, " cwd=%q", tab.CWD)
		}
		if tab.Durable {
			output.WriteString(" durable")
		}
		if tab.Exited {
			output.WriteString(" exited")
		}
	}
	return Output{OK: true, Text: output.String(), Terminals: append([]TerminalBrief(nil), page...)}
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

// operationResult queries the durable, owner-scoped outcome ledger for one
// operation's recorded outcome: its state, failure stage, residual session
// or resource, error, and the recorded result payload — the pre-retry
// answer for an uncertain mutation.
func (r *Registry) operationResult(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		OperationID string `json:"operation_id"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if strings.TrimSpace(args.OperationID) == "" {
		return Fail(invalid("operation_id 不能为空"))
	}
	if r.deps.DurableOutcomes == nil {
		return Fail(errors.New("结果查询未配置"))
	}
	record, err := r.deps.DurableOutcomes.OutcomeResult(ctx, args.OperationID)
	if err != nil {
		return Fail(err)
	}
	var detail strings.Builder
	fmt.Fprintf(&detail, "操作 %s：state=%s kind=%s", record.OperationID, record.State, record.Kind)
	if record.FailureStage != "" {
		fmt.Fprintf(&detail, "，失败阶段=%s", record.FailureStage)
	}
	if record.ResidualSessionID != "" {
		fmt.Fprintf(&detail, "，残留会话 session_id=%s（未自动清理）", record.ResidualSessionID)
	}
	if record.ResidualResource != "" {
		fmt.Fprintf(&detail, "，残留资源=%s", record.ResidualResource)
	}
	if record.Error != "" {
		fmt.Fprintf(&detail, "，错误=%s", record.Error)
	}
	if len(record.Result) > 0 && string(record.Result) != "{}" {
		detail.WriteString("，结果=" + string(record.Result))
	}
	return OK(detail.String())
}

// tabDetail reads the optional per-tab identity detail for read_screen
// headers and context injection; the zero detail degrades silently.
func (r *Registry) tabDetail(ctx context.Context, tabID string) TabDetail {
	if terminal, ok := r.deps.Terminal.(TabDetailTerminal); ok {
		detail, err := terminal.TabDetail(ctx, tabID)
		if err == nil {
			return detail
		}
	}
	return TabDetail{}
}
