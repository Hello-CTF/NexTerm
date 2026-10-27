package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/terminal/staterule"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Marking intents: the user says the shown state is wrong, or that the
// screen is one that needs them.
const (
	stateRuleIntentNotThis  = "not_this"
	stateRuleIntentThisIsIt = "this_is_it"
)

const (
	// stateRuleDraftRows is how many bottom rows of the screen the model sees.
	stateRuleDraftRows = 30
	// stateRuleDraftTimeout bounds one drafting call to the model.
	stateRuleDraftTimeout = 60 * time.Second
	// stateRuleDraftReplyBytes bounds the reply that is parsed.
	stateRuleDraftReplyBytes = 16 << 10
)

// stateRuleModelFactory returns the chat model that drafts rules.
type stateRuleModelFactory func(ctx context.Context) (model.BaseChatModel, uint64, error)

// stateRuleScreenDTO is a screen as terminal_snapshot gives it, plus the
// window title.
type stateRuleScreenDTO struct {
	Lines     []string `json:"lines"`
	CursorRow int      `json:"cursorRow"`
	Title     string   `json:"title,omitempty"`
}

func (s stateRuleScreenDTO) screen() staterule.Screen {
	return staterule.Screen{Lines: s.Lines, CursorRow: s.CursorRow, Title: s.Title}
}

type stateRuleDraftRequest struct {
	// Intent is not_this or this_is_it.
	Intent string `json:"intent"`
	// Current is the state the session shows now.
	Current string             `json:"current,omitempty"`
	Screen  stateRuleScreenDTO `json:"screen"`
}

// stateRuleDraftDTO is a rule the model proposes. It is not validated or
// stored: the user reviews it, and state_rule_validate checks it.
type stateRuleDraftDTO struct {
	Name        string `json:"name"`
	Region      string `json:"region"`
	Lines       int32  `json:"lines,omitempty"`
	Pattern     string `json:"pattern"`
	State       string `json:"state"`
	Explanation string `json:"explanation,omitempty"`
	// Redacted reports that suspected secrets were masked in the screen text
	// before it was sent to the model.
	Redacted bool `json:"redacted,omitempty"`
}

// stateRuleDraftTargets are the states a draft for intent may set.
func stateRuleDraftTargets(intent, current string) ([]string, error) {
	switch intent {
	case stateRuleIntentThisIsIt:
		return []string{base.SessionNeedsMe}, nil
	case stateRuleIntentNotThis:
		var targets []string
		for _, state := range []string{base.SessionRunning, base.SessionIdle, base.SessionJustFinished, base.SessionNeedsMe} {
			if state != current {
				targets = append(targets, state)
			}
		}
		return targets, nil
	}
	return nil, ipc.BadParam(fmt.Errorf("未知的标记意图 %q", intent))
}

// stateRuleDraftPrompt builds the prompt and reports whether suspected
// secrets were masked in the screen text it quotes.
func stateRuleDraftPrompt(request stateRuleDraftRequest, targets []string) (string, bool) {
	rawExcerpt := staterule.RegionText(request.Screen.screen(), staterule.Rule{Region: staterule.RegionBottom, Lines: stateRuleDraftRows})
	rawTitle := staterule.RegionText(request.Screen.screen(), staterule.Rule{Region: staterule.RegionTitle})
	excerpt, title := tools.RedactText(rawExcerpt), tools.RedactText(rawTitle)
	redacted := excerpt != rawExcerpt || title != rawTitle
	var b strings.Builder
	b.WriteString("You write one state rule for a terminal app. A rule says: when a region of the terminal screen matches a regular expression, the session is in a given state.\n")
	b.WriteString("States: needs_me (the program waits for the user's answer or input), running (it is working), idle (a shell prompt, nothing to do), just_finished (a task just completed).\n")
	switch request.Intent {
	case stateRuleIntentThisIsIt:
		fmt.Fprintf(&b, "The app showed the session as %q, but the user says this screen needs them. Write a rule that recognizes this screen as needs_me.\n", request.Current)
	default:
		fmt.Fprintf(&b, "The app showed the session as %q, but the user says that is wrong for this screen. Write a rule that recognizes this screen as the correct state.\n", request.Current)
	}
	fmt.Fprintf(&b, "Allowed target states: %s.\n", strings.Join(targets, ", "))
	fmt.Fprintf(&b, "Regions: \"bottom\" is the last `lines` rows of the screen (1 to %d, rows joined with \\n); \"title\" is the window title.\n", staterule.MaxLines)
	fmt.Fprintf(&b, "The pattern is a Go RE2 regular expression (no lookaround, no backreferences), at most %d bytes, and must not match empty text. Match stable text that identifies this kind of screen, not values that change each time such as numbers, times, paths or file names.\n", staterule.MaxPatternBytes)
	b.WriteString("Reply with only one JSON object and nothing else: {\"name\": short description, \"region\": \"bottom\" or \"title\", \"lines\": number of bottom rows, \"pattern\": RE2 pattern, \"state\": target state, \"explanation\": one sentence}.\n")
	b.WriteString("\nWindow title:\n")
	b.WriteString(title)
	b.WriteString("\n\nBottom of the screen:\n<<<\n")
	b.WriteString(excerpt)
	b.WriteString("\n>>>\n")
	return b.String(), redacted
}

// parseStateRuleDraft reads the JSON object of a model reply, which may sit
// in a code fence or among other text.
func parseStateRuleDraft(reply string, targets []string) (stateRuleDraftDTO, error) {
	if len(reply) > stateRuleDraftReplyBytes {
		reply = reply[:stateRuleDraftReplyBytes]
	}
	start, end := strings.Index(reply, "{"), strings.LastIndex(reply, "}")
	if start < 0 || end < start {
		return stateRuleDraftDTO{}, errors.New("AI 的回复里没有规则")
	}
	var draft stateRuleDraftDTO
	if err := json.Unmarshal([]byte(reply[start:end+1]), &draft); err != nil {
		return stateRuleDraftDTO{}, fmt.Errorf("AI 的回复不是有效的规则: %w", err)
	}
	draft.Name = strings.TrimSpace(draft.Name)
	draft.Region = strings.TrimSpace(draft.Region)
	draft.State = strings.TrimSpace(draft.State)
	draft.Explanation = strings.TrimSpace(draft.Explanation)
	if draft.Pattern == "" {
		return stateRuleDraftDTO{}, errors.New("AI 起草的规则没有匹配模式")
	}
	allowed := false
	for _, state := range targets {
		allowed = allowed || state == draft.State
	}
	if !allowed {
		return stateRuleDraftDTO{}, fmt.Errorf("AI 起草的目标状态 %q 与标记不符（应为 %s）", draft.State, strings.Join(targets, "、"))
	}
	return draft, nil
}

// draft asks the model for a rule that recognizes the screen as the user
// says it is. It never stores anything.
func (r *stateRulesRuntime) draft(ctx context.Context, request stateRuleDraftRequest) (stateRuleDraftDTO, error) {
	targets, err := stateRuleDraftTargets(request.Intent, request.Current)
	if err != nil {
		return stateRuleDraftDTO{}, err
	}
	if len(request.Screen.Lines) == 0 && strings.TrimSpace(request.Screen.Title) == "" {
		return stateRuleDraftDTO{}, ipc.BadParam(errors.New("屏幕为空，没有可起草的内容"))
	}
	if r.draftModel == nil {
		return stateRuleDraftDTO{}, ipc.NewError(ipc.CodeUnsupported, "AI 模型不可用")
	}
	ctx, cancel := context.WithTimeout(ctx, stateRuleDraftTimeout)
	defer cancel()
	chatModel, _, err := r.draftModel(ctx)
	if err != nil {
		return stateRuleDraftDTO{}, ipc.WrapError(ipc.CodeUnsupported, "AI 模型不可用: "+err.Error(), err)
	}
	prompt, redacted := stateRuleDraftPrompt(request, targets)
	response, err := chatModel.Generate(ctx, []*schema.Message{schema.UserMessage(prompt)})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return stateRuleDraftDTO{}, ipc.WrapError(ipc.CodeTimeout, "AI 起草超时", err)
		}
		return stateRuleDraftDTO{}, ipc.WrapError(ipc.CodeIO, "AI 起草失败: "+err.Error(), err)
	}
	if response == nil {
		return stateRuleDraftDTO{}, ipc.NewError(ipc.CodeIO, "AI 起草返回空响应")
	}
	draft, err := parseStateRuleDraft(response.Content, targets)
	if err != nil {
		return stateRuleDraftDTO{}, ipc.WrapError(ipc.CodeBadParam, err.Error(), err)
	}
	draft.Redacted = redacted
	return draft, nil
}
