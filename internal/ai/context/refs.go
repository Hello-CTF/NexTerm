package aicontext

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

const (
	RefMaxCount    = 4
	RefScreenRunes = 4000
	RefFileRunes   = 6000
	RefTotalRunes  = 12000
)

type RefSpec struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Label     string `json:"label"`
	SessionID string `json:"sessionId,omitempty"`
	Path      string `json:"path,omitempty"`
	// Text carries the quoted content of a "text" reference, such as a command block's output.
	Text string `json:"text,omitempty"`
}

type RefFileReader func(ctx context.Context, sessionID, path string) (string, error)

// RenderRefs expands @-referenced objects into real content injected into the model: terminal tabs take
// the current screen, text takes the quoted command output (tail-truncated) (tail-truncated to keep the latest output), sessions take the connection summary,
// and files take content (head-truncated). All subject to the RefTotalRunes budget; per-item failures are annotated and skipped.
func (b *Builder) RenderRefs(ctx context.Context, refs []RefSpec, readFile RefFileReader) string {
	if len(refs) == 0 {
		return ""
	}
	if len(refs) > RefMaxCount {
		refs = refs[:RefMaxCount]
	}
	var output strings.Builder
	remaining := RefTotalRunes
	for _, ref := range refs {
		if remaining <= 0 {
			break
		}
		section := b.renderRef(ctx, ref, readFile, remaining)
		if section == "" {
			continue
		}
		output.WriteString(section)
		remaining -= len([]rune(section))
	}
	return output.String()
}

func (b *Builder) renderRef(ctx context.Context, ref RefSpec, readFile RefFileReader, budget int) string {
	label := singleLineLabel(ref.Label)
	if label == "" {
		label = ref.ID
	}
	switch ref.Kind {
	case "tab":
		if b.deps.Screen == nil || ref.ID == "" {
			return ""
		}
		screen, err := b.deps.Screen(ctx, ref.ID)
		if err != nil {
			return fmt.Sprintf("- %s (unable to read the terminal screen)\n", label)
		}
		if strings.TrimSpace(screen.Text) == "" {
			return fmt.Sprintf("- %s (terminal screen is empty)\n", label)
		}
		return fmt.Sprintf("[Reference: terminal screen of %s]\n%s\n", label, truncateRunes(screen.Text, min(RefScreenRunes, budget), true))
	case "text":
		if strings.TrimSpace(ref.Text) == "" {
			return fmt.Sprintf("- %s (empty)\n", label)
		}
		return fmt.Sprintf("[Reference: command output %s]\n%s\n", label, truncateRunes(ref.Text, min(RefScreenRunes, budget), true))
	case "asset":
		if b.deps.Session == nil || ref.ID == "" {
			return ""
		}
		session, err := b.deps.Session(ctx, ref.ID)
		if err != nil {
			return fmt.Sprintf("- %s (unable to read session info)\n", label)
		}
		var brief strings.Builder
		fmt.Fprintf(&brief, "- Connection: %s (%s)\n", session.Name, session.Kind)
		if session.Host != "" {
			fmt.Fprintf(&brief, "- Host: %s\n", session.Host)
		}
		if session.Username != "" {
			fmt.Fprintf(&brief, "- User: %s\n", session.Username)
		}
		if session.CWD != "" {
			fmt.Fprintf(&brief, "- Current Directory: %s\n", session.CWD)
		}
		return fmt.Sprintf("[Reference: session %s]\n%s", label, truncateRunes(brief.String(), budget, false))
	case "file":
		if readFile == nil || ref.Path == "" {
			return ""
		}
		content, err := readFile(ctx, ref.SessionID, ref.Path)
		if err != nil {
			return fmt.Sprintf("- %s (unable to read file %s)\n", label, ref.Path)
		}
		return fmt.Sprintf("[Reference: file %s]\n%s\n", ref.Path, truncateRunes(content, min(RefFileRunes, budget), false))
	default:
		return ""
	}
}

// singleLineLabel turns a client-supplied label into one plain line: every control character becomes a
// space and runs of whitespace collapse, so a label can neither break the reference header nor smuggle
// terminal escapes or extra lines into the prompt.
func singleLineLabel(label string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, label)), " ")
}

func truncateRunes(value string, limit int, keepTail bool) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if keepTail {
		return "[Earlier content truncated]\n" + string(runes[len(runes)-limit:])
	}
	return string(runes[:limit]) + "\n[Content truncated]"
}
