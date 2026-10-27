package production

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type draftChatModel struct {
	reply  string
	err    error
	prompt string
}

func (m *draftChatModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if len(input) > 0 {
		m.prompt = input[len(input)-1].Content
	}
	if m.err != nil {
		return nil, m.err
	}
	return schema.AssistantMessage(m.reply, nil), nil
}

func (m *draftChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func newStateRuleCommands(t *testing.T, chat model.BaseChatModel) (*ipc.Dispatcher, *store.Store) {
	t.Helper()
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runtime := newStateRulesRuntime(database, nil, "", false)
	if chat != nil {
		runtime.draftModel = func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 0, nil }
	}
	dispatcher := ipc.NewDispatcher()
	if err := runtime.registerCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return dispatcher, database
}

func dispatchStateRule(dispatcher *ipc.Dispatcher, ctx context.Context, command string, input any) ipc.Response {
	return dispatchFSTransferTest(dispatcher, ctx, command, input, nil)
}

var draftScreen = stateRuleScreenDTO{Lines: []string{"$ deploy", "Uploading 3 files", "Continue? [y/N] ", "", ""}, CursorRow: 2, Title: "deploy"}

func TestStateRuleValidateReportsFieldAndTestsTheScreen(t *testing.T) {
	dispatcher, _ := newStateRuleCommands(t, nil)
	ctx := context.Background()
	valid := stateRuleSaveRequest{Scope: store.StateRuleScopeGlobal, Region: "bottom", Lines: 2, Pattern: `Continue\? \[y/N\]`, State: "needs_me", Enabled: true}
	result := decodeFSTransferTest[stateRuleValidateResult](t, dispatchStateRule(dispatcher, ctx, "state_rule_validate", stateRuleValidateRequest{Rule: valid, Screen: &draftScreen}))
	if !result.Valid || !result.Tested || !result.Matched || result.Region != "Uploading 3 files\nContinue? [y/N] " {
		t.Fatalf("valid rule = %+v", result)
	}
	other := valid
	other.Pattern = "Overwrite"
	result = decodeFSTransferTest[stateRuleValidateResult](t, dispatchStateRule(dispatcher, ctx, "state_rule_validate", stateRuleValidateRequest{Rule: other, Screen: &draftScreen}))
	if !result.Valid || !result.Tested || result.Matched {
		t.Fatalf("non-matching rule = %+v", result)
	}
	result = decodeFSTransferTest[stateRuleValidateResult](t, dispatchStateRule(dispatcher, ctx, "state_rule_validate", stateRuleValidateRequest{Rule: valid}))
	if !result.Valid || result.Tested || result.Lines != 2 {
		t.Fatalf("rule without a screen = %+v", result)
	}

	for want, rule := range map[string]stateRuleSaveRequest{
		"pattern": {Scope: store.StateRuleScopeGlobal, Region: "bottom", Pattern: `(a)\1`, State: "idle"},
		"state":   {Scope: store.StateRuleScopeGlobal, Region: "bottom", Pattern: "a", State: "exited"},
		"keys":    {Scope: store.StateRuleScopeGlobal, Region: "bottom", Pattern: "a", State: "idle", Keys: "y"},
		"scope":   {Scope: store.StateRuleScopeHost, Region: "bottom", Pattern: "a", State: "idle"},
		"lines":   {Scope: store.StateRuleScopeGlobal, Region: "bottom", Lines: 99, Pattern: "a", State: "idle"},
	} {
		result := decodeFSTransferTest[stateRuleValidateResult](t, dispatchStateRule(dispatcher, ctx, "state_rule_validate", stateRuleValidateRequest{Rule: rule, Screen: &draftScreen}))
		if result.Valid || result.Field != want || result.Error == "" || result.Tested {
			t.Fatalf("%s: result = %+v", want, result)
		}
	}
}

func TestStateRuleReorderCommandReturnsTheNewOrder(t *testing.T) {
	dispatcher, _ := newStateRuleCommands(t, nil)
	ctx := context.Background()
	var saved []stateRuleDTO
	for _, pattern := range []string{"one", "two"} {
		saved = append(saved, decodeFSTransferTest[stateRuleDTO](t, dispatchStateRule(dispatcher, ctx, "state_rule_save", stateRuleSaveRequest{
			Scope: store.StateRuleScopeGlobal, Region: "bottom", Pattern: pattern, State: "idle", Enabled: true,
		})))
	}
	rows := decodeFSTransferTest[[]stateRuleDTO](t, dispatchStateRule(dispatcher, ctx, "state_rule_reorder", stateRuleReorderRequest{IDs: []string{saved[1].ID, saved[0].ID}}))
	if len(rows) != 2 || rows[0].ID != saved[1].ID || rows[1].ID != saved[0].ID {
		t.Fatalf("reordered = %+v", rows)
	}
	response := dispatchStateRule(dispatcher, ctx, "state_rule_reorder", stateRuleReorderRequest{IDs: []string{saved[0].ID}})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("partial order response = %+v", response)
	}
}

func TestStateRuleDraftAsksTheModelAndNeverSaves(t *testing.T) {
	chat := &draftChatModel{reply: "Here is the rule:\n```json\n{\"name\":\"deploy confirm\",\"region\":\"bottom\",\"lines\":2,\"pattern\":\"Continue\\\\? \\\\[y/N\\\\]\",\"state\":\"needs_me\",\"explanation\":\"asks to continue\"}\n```"}
	dispatcher, database := newStateRuleCommands(t, chat)
	ctx := context.Background()
	draft := decodeFSTransferTest[stateRuleDraftDTO](t, dispatchStateRule(dispatcher, ctx, "state_rule_draft", stateRuleDraftRequest{
		Intent: stateRuleIntentThisIsIt, Current: "running", Screen: draftScreen,
	}))
	if draft.Pattern != `Continue\? \[y/N\]` || draft.State != "needs_me" || draft.Region != "bottom" || draft.Lines != 2 || draft.Name != "deploy confirm" {
		t.Fatalf("draft = %+v", draft)
	}
	if !strings.Contains(chat.prompt, "Continue? [y/N]") || !strings.Contains(chat.prompt, "needs_me") {
		t.Fatalf("prompt lacks the screen or the target:\n%s", chat.prompt)
	}
	if rows, err := database.StateRuleList(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("drafting stored %d rules, %v", len(rows), err)
	}

	// A draft for "not this" may not keep the state the user rejected.
	response := dispatchStateRule(dispatcher, ctx, "state_rule_draft", stateRuleDraftRequest{
		Intent: stateRuleIntentNotThis, Current: "needs_me", Screen: draftScreen,
	})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("draft keeping the rejected state = %+v", response)
	}
	chat.reply = "I cannot help with that."
	response = dispatchStateRule(dispatcher, ctx, "state_rule_draft", stateRuleDraftRequest{Intent: stateRuleIntentThisIsIt, Screen: draftScreen})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("reply without a rule = %+v", response)
	}
	chat.err = errors.New("provider down")
	response = dispatchStateRule(dispatcher, ctx, "state_rule_draft", stateRuleDraftRequest{Intent: stateRuleIntentThisIsIt, Screen: draftScreen})
	if response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "provider down") {
		t.Fatalf("model failure = %+v", response)
	}
	response = dispatchStateRule(dispatcher, ctx, "state_rule_draft", stateRuleDraftRequest{Intent: "maybe", Screen: draftScreen})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("unknown intent = %+v", response)
	}
}

func TestStateRuleDraftWithoutAModelIsUnsupported(t *testing.T) {
	dispatcher, _ := newStateRuleCommands(t, nil)
	response := dispatchStateRule(dispatcher, context.Background(), "state_rule_draft", stateRuleDraftRequest{Intent: stateRuleIntentThisIsIt, Screen: draftScreen})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("draft without a model = %+v", response)
	}
}

func TestStateRuleDraftRedactsSecretsBeforeThePrompt(t *testing.T) {
	chat := &draftChatModel{reply: `{"name":"n","region":"bottom","lines":2,"pattern":"Continue","state":"needs_me"}`}
	dispatcher, _ := newStateRuleCommands(t, chat)
	screen := stateRuleScreenDTO{
		Lines:     []string{"$ export TOKEN=abc123secret", "$ mysql -pSecret0 -u root", "Continue? [y/N] "},
		CursorRow: 2,
		Title:     "deploy password=hunter2",
	}
	draft := decodeFSTransferTest[stateRuleDraftDTO](t, dispatchStateRule(dispatcher, context.Background(), "state_rule_draft", stateRuleDraftRequest{
		Intent: stateRuleIntentThisIsIt, Current: "running", Screen: screen,
	}))
	for _, secret := range []string{"abc123secret", "Secret0", "hunter2"} {
		if strings.Contains(chat.prompt, secret) {
			t.Fatalf("prompt leaks %q:\n%s", secret, chat.prompt)
		}
	}
	if !strings.Contains(chat.prompt, "Continue? [y/N]") || !draft.Redacted {
		t.Fatalf("prompt = %s, draft = %+v", chat.prompt, draft)
	}

	// A clean screen is not reported as redacted.
	draft = decodeFSTransferTest[stateRuleDraftDTO](t, dispatchStateRule(dispatcher, context.Background(), "state_rule_draft", stateRuleDraftRequest{
		Intent: stateRuleIntentThisIsIt, Current: "running", Screen: draftScreen,
	}))
	if draft.Redacted {
		t.Fatalf("clean screen reported as redacted: %+v", draft)
	}
}

func TestStateRuleValidateReportsSetCostWithoutAScreen(t *testing.T) {
	dispatcher, _ := newStateRuleCommands(t, nil)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		rule stateRuleSaveRequest
		want string
	}{
		"lines": {stateRuleSaveRequest{Scope: store.StateRuleScopeGlobal, Region: "bottom", Lines: 50, Pattern: `[a-z]{400}`, State: "idle"}, "lines"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.ValidateStateRuleInput(store.StateRuleInput{Scope: tc.rule.Scope, Region: tc.rule.Region, Lines: tc.rule.Lines, Pattern: tc.rule.Pattern, State: tc.rule.State}); err != nil {
				t.Fatalf("rule is not valid on its own: %v", err)
			}
			result := decodeFSTransferTest[stateRuleValidateResult](t, dispatchStateRule(dispatcher, ctx, "state_rule_validate", stateRuleValidateRequest{Rule: tc.rule}))
			if result.Valid || result.Field != tc.want || result.Error == "" {
				t.Fatalf("result = %+v", result)
			}
			response := dispatchStateRule(dispatcher, ctx, "state_rule_save", tc.rule)
			if response.OK {
				t.Fatal("save accepted a rule that validate rejects")
			}
		})
	}
}

func TestStateRuleDraftNeverCarriesKeys(t *testing.T) {
	chat := &draftChatModel{reply: `{"name":"n","region":"bottom","lines":2,"pattern":"Continue","state":"needs_me","keys":"y\n"}`}
	dispatcher, _ := newStateRuleCommands(t, chat)
	response := dispatchStateRule(dispatcher, context.Background(), "state_rule_draft", stateRuleDraftRequest{
		Intent: stateRuleIntentThisIsIt, Current: "running", Screen: draftScreen,
	})
	if !response.OK {
		t.Fatalf("draft = %+v", response)
	}
	var fields map[string]any
	if err := json.Unmarshal(response.Data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, has := fields["keys"]; has || fields["pattern"] != "Continue" {
		t.Fatalf("draft fields = %v, want a pattern and no keys", fields)
	}
}
