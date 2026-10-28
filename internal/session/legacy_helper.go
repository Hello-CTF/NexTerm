package session

import (
	"context"
	"errors"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

// LegacyHelperDetail is what the user sees before agreeing to end an older
// helper: the helper as inspected on its host and the sessions it runs. It
// is the detail of a helper_upgrade_required error and of a helper status.
type LegacyHelperDetail struct {
	Protocol int `json:"protocol"`
	PID      int `json:"pid,omitempty"`
	// PTYSessions and ExecSessions count the sessions ending the helper
	// would end. ActivityKnown is false when the helper did not report
	// them; PTYListed then means PTYSessions comes from its own list and
	// the exec count is unknown.
	PTYSessions   int    `json:"ptySessions"`
	ExecSessions  int    `json:"execSessions"`
	ActivityKnown bool   `json:"activityKnown"`
	PTYListed     bool   `json:"ptyListed"`
	Release       string `json:"release,omitempty"`
	// ConsentToken names exactly this helper and count; it is empty when the
	// helper could not be inspected, and then nothing can be approved.
	ConsentToken string `json:"consentToken,omitempty"`
	// Stale reports that an approval no longer matched the helper.
	Stale bool `json:"stale,omitempty"`
}

// LegacyHelperDetailFor returns the older helper an error reports, or nil.
func LegacyHelperDetailFor(err error) *LegacyHelperDetail {
	var legacy *supervisor.LegacyHelperError
	if !errors.As(err, &legacy) {
		return nil
	}
	detail := &LegacyHelperDetail{
		Protocol: legacy.Helper.Protocol, PID: legacy.Helper.PID,
		PTYSessions: legacy.Helper.Activity.PTY, ExecSessions: legacy.Helper.Activity.Exec,
		ActivityKnown: legacy.Helper.ActivityKnown, PTYListed: legacy.Helper.PTYListed,
		ConsentToken: legacy.ConsentToken, Stale: legacy.Stale,
	}
	if legacy.Helper.Runtime != nil {
		detail.Release = legacy.Helper.Runtime.Version
	}
	return detail
}

// HelperReplaceFailure is the detail of a helper_replace_failed error: the
// stage that failed and what had already happened to the older helper.
type HelperReplaceFailure struct {
	Stage      string `json:"stage"`
	Stopped    bool   `json:"stopped"`
	Forced     bool   `json:"forced"`
	RolledBack bool   `json:"rolledBack"`
	Cause      string `json:"cause,omitempty"`
}

// HelperReplaceFailureFor returns the failed replacement an error reports,
// or nil.
func HelperReplaceFailureFor(err error) *HelperReplaceFailure {
	var failed *supervisor.HelperReplaceError
	if !errors.As(err, &failed) {
		return nil
	}
	detail := &HelperReplaceFailure{Stage: string(failed.Stage), Stopped: failed.Stopped, Forced: failed.Forced, RolledBack: failed.RolledBack}
	if failed.Cause != nil {
		detail.Cause = failed.Cause.Error()
	}
	return detail
}

// ForcedStopNote is shown wherever a consented replacement had to kill the
// older helper.
const ForcedStopNote = "旧版组件未响应退出请求，已被强制结束，部分后台进程可能仍在运行"

func helperReplaceMessage(detail *HelperReplaceFailure) string {
	var message string
	switch supervisor.ReplaceStage(detail.Stage) {
	case supervisor.ReplaceVerify:
		message = "无法核实主机上的旧版组件，未做任何更改"
	case supervisor.ReplaceTerminate:
		message = "未能结束旧版组件，它和其中的会话仍在运行；未启动新版持久终端组件"
	case supervisor.ReplaceStart:
		switch {
		case detail.Stopped && detail.RolledBack:
			message = "旧版组件已结束（其中的会话已终止），但新版持久终端组件未能启动；已重新启动旧版组件，原会话不会恢复"
		case detail.Stopped:
			message = "旧版组件已结束（其中的会话已终止），但新版持久终端组件未能启动"
		default:
			message = "新版持久终端组件未能启动"
		}
	default:
		message = "升级持久终端组件失败"
	}
	if detail.Forced {
		message += "；" + ForcedStopNote
	}
	if detail.Cause != "" {
		message += "（" + detail.Cause + "）"
	}
	return message
}

// legacyHelperBanner ends a durable tab whose session still runs in an older
// (protocol 2) helper that this build cannot attach to. The session itself
// keeps running there until the user agrees to replace that helper.
var legacyHelperBanner = []byte("\r\n\x1b[33m[NexTerm] 旧版组件：此持久终端仍在主机上的旧版持久终端组件中运行，当前版本无法接入。" +
	"在持久终端组件窗口确认升级后它会被终止；不升级时它继续在旧版组件中运行。\x1b[0m\r\n")

// legacyHelperTerminatedBanner follows legacyHelperBanner once the user agreed
// to replace the older helper: the session it ran was ended with it.
var legacyHelperTerminatedBanner = []byte("\r\n\x1b[31m[NexTerm] 已终止：已按确认升级持久终端组件，旧版组件及此终端的会话已结束；" +
	"其输出记录保留为只读。\x1b[0m\r\n")

// legacyEndedTabs remembers the tabs of each session that ended with the
// legacy banner, so a consented replacement can tell them what happened.
// Its lock is never held while a tab lock is taken.
type legacyEndedTabs struct {
	mu   sync.Mutex
	tabs map[string]map[*Tab]uint64
}

func (l *legacyEndedTabs) add(sessionID string, tab *Tab, generation uint64) {
	l.prune()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.tabs == nil {
		l.tabs = make(map[string]map[*Tab]uint64)
	}
	if l.tabs[sessionID] == nil {
		l.tabs[sessionID] = make(map[*Tab]uint64)
	}
	l.tabs[sessionID][tab] = generation
}

// prune drops tabs that were closed since they ended. Tab locks are taken
// only while l.mu is not held, so the two are never nested.
func (l *legacyEndedTabs) prune() {
	l.mu.Lock()
	remembered := make([]*Tab, 0, len(l.tabs))
	for _, tabs := range l.tabs {
		for tab := range tabs {
			remembered = append(remembered, tab)
		}
	}
	l.mu.Unlock()
	var closed []*Tab
	for _, tab := range remembered {
		tab.mu.Lock()
		if tab.closed {
			closed = append(closed, tab)
		}
		tab.mu.Unlock()
	}
	if len(closed) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for sessionID, tabs := range l.tabs {
		for _, tab := range closed {
			delete(tabs, tab)
		}
		if len(tabs) == 0 {
			delete(l.tabs, sessionID)
		}
	}
}

// forget drops the tabs of a session that was closed.
func (l *legacyEndedTabs) forget(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.tabs, sessionID)
}

func (l *legacyEndedTabs) take(sessionID string) map[*Tab]uint64 {
	l.prune()
	l.mu.Lock()
	defer l.mu.Unlock()
	tabs := l.tabs[sessionID]
	delete(l.tabs, sessionID)
	return tabs
}

// LegacyHelperReplaced tells the tabs of sessionID that ended because an
// older helper ran their sessions that the user replaced that helper, which
// ended those sessions. It returns how many tabs were told.
func (m *Manager) LegacyHelperReplaced(ctx context.Context, sessionID string) int {
	told := 0
	for tab, generation := range m.legacyEnded.take(sessionID) {
		if m.feed(ctx, tab, generation, legacyHelperTerminatedBanner) == nil {
			told++
		}
	}
	return told
}
