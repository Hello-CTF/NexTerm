package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	aicontext "github.com/Hello-CTF/NexTerm/internal/ai/context"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

func refSpecs(refs []RefArg) []aicontext.RefSpec {
	specs := make([]aicontext.RefSpec, 0, len(refs))
	for _, ref := range refs {
		specs = append(specs, aicontext.RefSpec{
			Kind:      ref.Kind,
			ID:        ref.ID,
			Label:     ref.Label,
			SessionID: ref.SessionID,
			Path:      ref.Path,
			Text:      ref.Text,
		})
	}
	return specs
}

func (r *Runner) refFileReader() aicontext.RefFileReader {
	if r.config.Tools == nil {
		return nil
	}
	return func(ctx context.Context, sessionID, path string) (string, error) {
		return r.config.Tools.ReadRefFile(ctx, tools.Scope{SessionID: sessionID}, path)
	}
}

// referencedTabLimit bounds the terminals a conversation keeps as @-referenced
// read-only targets; the oldest reference is dropped first.
const referencedTabLimit = 16

type referencedTab struct {
	id    string
	label string
}

// referencedTabSet holds the terminals the user @-referenced across the runs of
// one conversation, so a follow-up turn can still read them. It lives in
// memory only: after a restart the user references the terminal again.
type referencedTabSet struct {
	mu   sync.Mutex
	tabs []referencedTab
}

func (s *referencedTabSet) add(refs []RefArg) []referencedTab {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ref := range refs {
		if ref.Kind != "tab" || ref.ID == "" {
			continue
		}
		s.tabs = slices.DeleteFunc(s.tabs, func(tab referencedTab) bool { return tab.id == ref.ID })
		s.tabs = append(s.tabs, referencedTab{id: ref.ID, label: aicontext.SingleLineLabel(ref.Label)})
	}
	if len(s.tabs) > referencedTabLimit {
		s.tabs = slices.Delete(s.tabs, 0, len(s.tabs)-referencedTabLimit)
	}
	return slices.Clone(s.tabs)
}

// grantReferencedTabs merges this run's @-referenced terminals into the
// conversation's set, grants them to the job as read-only targets, and
// returns the prompt that tells the model the handle of each granted tab.
func (r *Runner) grantReferencedTabs(current *job) string {
	if r.config.Tools == nil {
		return ""
	}
	set := &referencedTabSet{}
	if current.args.ConversationID != "" {
		key := current.ownerID + "\x00" + current.args.ConversationID
		stored, _ := r.referencedTabs.LoadOrStore(key, set)
		set = stored.(*referencedTabSet)
	}
	tabs := set.add(current.args.Refs)
	if len(tabs) == 0 {
		return ""
	}
	ids := make([]string, 0, len(tabs))
	labels := make(map[string]string, len(tabs))
	for _, tab := range tabs {
		ids = append(ids, tab.id)
		labels[tab.id] = tab.label
	}
	granted := r.config.Tools.GrantReferencedTabs(current.ctx, current.id, current.args.Scope, ids)
	if len(granted) == 0 {
		return ""
	}
	var prompt strings.Builder
	prompt.WriteString("[Referenced Terminals]\nThe user @-referenced these terminals in this conversation. Pass the handle as tab_id.\n")
	for _, terminal := range granted {
		label := labels[terminal.Handle.TabID]
		if label == "" {
			label = terminal.Handle.TabID
		}
		access := "read-write under the usual approval rules"
		if terminal.ReadOnly {
			access = "read-only: read_screen and wait_for only; send_keys and terminal_rename are refused"
		}
		fmt.Fprintf(&prompt, "- %s: %s (%s)\n", label, terminal.Handle.String(), access)
	}
	return prompt.String()
}
