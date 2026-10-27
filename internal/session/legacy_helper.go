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

// legacyHelperBanner ends a durable tab whose session still runs in an older
// (protocol 2) helper that this build cannot attach to. The session itself
// keeps running there until the user agrees to replace that helper.
var legacyHelperBanner = []byte("\r\n\x1b[33m[NexTerm] 旧版组件：此持久终端仍在主机上的旧版持久终端组件中运行，当前版本无法接入。" +
	"在持久终端组件窗口确认升级后它会被终止；不升级时它继续在旧版组件中运行。\x1b[0m\r\n")

// legacyHelperTerminatedBanner follows legacyHelperBanner once the user agreed
// to replace the older helper: the session it ran was ended with it.
var legacyHelperTerminatedBanner = []byte("\r\n\x1b[31m[NexTerm] 已终止：已按你的确认升级持久终端组件，旧版组件及此终端的会话已结束；" +
	"其输出记录保留为只读。\x1b[0m\r\n")

// legacyEndedTabs remembers the tabs of each session that ended with the
// legacy banner, so a consented replacement can tell them what happened.
type legacyEndedTabs struct {
	mu   sync.Mutex
	tabs map[string]map[*Tab]uint64
}

func (l *legacyEndedTabs) add(sessionID string, tab *Tab, generation uint64) {
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

func (l *legacyEndedTabs) take(sessionID string) map[*Tab]uint64 {
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
