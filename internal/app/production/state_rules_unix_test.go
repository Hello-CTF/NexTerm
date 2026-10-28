//go:build unix

package production

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const testStateRulesIdentity = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type stateRuleFixture struct {
	database   *store.Store
	runtime    *stateRulesRuntime
	dispatcher *ipc.Dispatcher
	helper     *supervisor.Supervisor
	server     *supervisor.Server
}

func newStateRuleFixture(t *testing.T, multiAccount bool) stateRuleFixture {
	t.Helper()
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runtime := newStateRulesRuntime(database, nil, testStateRulesIdentity, multiAccount)
	dispatcher := ipc.NewDispatcher()
	if err := runtime.registerCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	helper, err := supervisor.New(supervisor.Config{StateDir: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = helper.Close() })
	runDir, err := os.MkdirTemp("/tmp", "nx-rules-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	server, err := supervisor.NewServer(helper, filepath.Join(runDir, "supervisor.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	return stateRuleFixture{database: database, runtime: runtime, dispatcher: dispatcher, helper: helper, server: server}
}

func (f stateRuleFixture) asset(t *testing.T, ctx context.Context, name string) string {
	t.Helper()
	asset, err := f.database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: name, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	return asset.ID
}

// provider resolves a helper provider for assetID as the resolver does.
func (f stateRuleFixture) provider(ctx context.Context, assetID string) base.DurableProvider {
	resolveCtx := ipc.WithHelperProgressScope(ctx, ipc.HelperProgressScope{AssetID: assetID})
	return f.runtime.wrapProvider(resolveCtx, assetID, supervisor.NewRemoteProvider(supervisor.NewClient(f.server.SocketPath(), f.helper.StateDir())))
}

func (f stateRuleFixture) dispatch(t *testing.T, ctx context.Context, command string, input any) ipc.Response {
	t.Helper()
	return dispatchFSTransferTest(f.dispatcher, ctx, command, input, nil)
}

// awaitRevision waits for the asynchronous push a rule change starts.
func (f stateRuleFixture) awaitRevision(t *testing.T, account string, want func(string) bool) string {
	t.Helper()
	key := stateRulesOwnerKey(testStateRulesIdentity, account)
	deadline := time.Now().Add(10 * time.Second)
	for {
		revision := f.helper.StateRulesRevision(key)
		if want(revision) {
			return revision
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper revision of %q stayed %q", account, revision)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// awaitStatus waits for the runtime to record the outcome of a push.
func (f stateRuleFixture) awaitStatus(t *testing.T, account string, want func([]stateRuleHelperStatusDTO) bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		status := f.runtime.status(account)
		if want(status) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status of %q stayed %+v", account, status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func listDurable(t *testing.T, provider base.DurableProvider) {
	t.Helper()
	if _, err := provider.ListDurableDescriptors(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStateRuleChangesReachAnOnlineHelper(t *testing.T) {
	f := newStateRuleFixture(t, false)
	ctx := context.Background()
	assetID := f.asset(t, ctx, "agent-host")
	otherAsset := f.asset(t, ctx, "other-host")
	// Any helper operation registers the helper and confirms it.
	listDurable(t, f.provider(ctx, assetID))
	if status := f.runtime.status(""); len(status) != 1 || status[0].Status != stateRuleStatusApplied || status[0].AssetID != assetID {
		t.Fatalf("status before rules = %+v", status)
	}

	hostRule := decodeFSTransferTest[stateRuleDTO](t, f.dispatch(t, ctx, "state_rule_save", stateRuleSaveRequest{
		Name: "review", Scope: store.StateRuleScopeHost, AssetID: assetID, Region: "bottom",
		Pattern: "Waiting for your review", State: "needs_me", Keys: "\r", Enabled: true,
	}))
	otherHost := decodeFSTransferTest[stateRuleDTO](t, f.dispatch(t, ctx, "state_rule_save", stateRuleSaveRequest{
		Scope: store.StateRuleScopeHost, AssetID: otherAsset, Region: "title", Pattern: "elsewhere", State: "idle", Enabled: true,
	}))
	revision := f.awaitRevision(t, "", func(revision string) bool { return revision != "" })
	rules := f.helper.StateRules(stateRulesOwnerKey(testStateRulesIdentity, "")).Rules
	if len(rules) != 1 || rules[0].ID != hostRule.ID || rules[0].Keys != "\r" || rules[0].Lines != 5 {
		t.Fatalf("helper rules = %+v (other host rule %s must not apply)", rules, otherHost.ID)
	}
	// The helper holds the revision before the push reply reaches the
	// runtime, so the recorded status can trail it briefly.
	f.awaitStatus(t, "", func(status []stateRuleHelperStatusDTO) bool {
		return len(status) == 1 && status[0].Status == stateRuleStatusApplied && status[0].Revision == revision
	})

	// Invalid rules, and host rules for an unknown host, are rejected
	// visibly and the helper keeps its rules.
	response := f.dispatch(t, ctx, "state_rule_save", stateRuleSaveRequest{Scope: store.StateRuleScopeGlobal, Region: "bottom", Pattern: `(a)\1`, State: "idle", Enabled: true})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam || !strings.Contains(response.Error.Message, "匹配模式") {
		t.Fatalf("invalid rule response = %+v", response)
	}
	response = f.dispatch(t, ctx, "state_rule_save", stateRuleSaveRequest{Scope: store.StateRuleScopeHost, AssetID: ids.New(), Region: "bottom", Pattern: "a", State: "idle", Enabled: true})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("unknown host response = %+v", response)
	}

	// Export and import round-trip; deleting the rule clears the helper.
	exported := decodeFSTransferTest[stateRuleExportDTO](t, f.dispatch(t, ctx, "state_rule_export", struct{}{}))
	if exported.Format != stateRuleExportFormat || len(exported.Rules) != 2 || exported.Rules[0].ID != hostRule.ID {
		t.Fatalf("export = %+v", exported)
	}
	result := decodeFSTransferTest[stateRuleImportResultDTO](t, f.dispatch(t, ctx, "state_rule_import", stateRuleImportRequest(exported)))
	if result != (stateRuleImportResultDTO{Unchanged: 2}) {
		t.Fatalf("re-import = %+v", result)
	}
	if response := f.dispatch(t, ctx, "state_rule_import", stateRuleImportRequest{Format: "other", Version: 1}); response.OK {
		t.Fatal("a foreign file was imported")
	}
	decodeFSTransferTest[stateRuleDeleteResult](t, f.dispatch(t, ctx, "state_rule_delete", stateRuleDeleteRequest{ID: hostRule.ID}))
	f.awaitRevision(t, "", func(revision string) bool { return revision == "" })
}

func TestStateRulesOfAccountsStayApartOnOneHelper(t *testing.T) {
	f := newStateRuleFixture(t, true)
	ctxA := ipc.WithUserID(context.Background(), "rules-account-a")
	ctxB := ipc.WithUserID(context.Background(), "rules-account-b")
	assetA := f.asset(t, ctxA, "shared-host-a")
	assetB := f.asset(t, ctxB, "shared-host-b")
	// Both accounts reach the same helper, as through one SSH user.
	providerA := f.provider(ctxA, assetA)
	providerB := f.provider(ctxB, assetB)
	listDurable(t, providerA)
	listDurable(t, providerB)

	ruleA := decodeFSTransferTest[stateRuleDTO](t, f.dispatch(t, ctxA, "state_rule_save", stateRuleSaveRequest{
		Scope: store.StateRuleScopeGlobal, Region: "bottom", Pattern: "Waiting", State: "needs_me", Enabled: true,
	}))
	f.awaitRevision(t, "rules-account-a", func(revision string) bool { return revision != "" })
	// B has no rules; its connections leave A's alone.
	listDurable(t, providerB)
	if rules := f.helper.StateRules(stateRulesOwnerKey(testStateRulesIdentity, "rules-account-a")).Rules; len(rules) != 1 || rules[0].ID != ruleA.ID {
		t.Fatalf("A's rules after B connected = %+v", rules)
	}
	if got := f.helper.StateRulesRevision(stateRulesOwnerKey(testStateRulesIdentity, "rules-account-b")); got != "" {
		t.Fatalf("B holds rules %q", got)
	}
	ruleB := decodeFSTransferTest[stateRuleDTO](t, f.dispatch(t, ctxB, "state_rule_save", stateRuleSaveRequest{
		Scope: store.StateRuleScopeGlobal, Region: "title", Pattern: "Thinking", State: "running", Enabled: true,
	}))
	f.awaitRevision(t, "rules-account-b", func(revision string) bool { return revision != "" })
	if rules := f.helper.StateRules(stateRulesOwnerKey(testStateRulesIdentity, "rules-account-b")).Rules; len(rules) != 1 || rules[0].ID != ruleB.ID {
		t.Fatalf("B's rules = %+v", rules)
	}
	if rules := f.helper.StateRules(stateRulesOwnerKey(testStateRulesIdentity, "rules-account-a")).Rules; len(rules) != 1 || rules[0].ID != ruleA.ID {
		t.Fatalf("A's rules after B pushed = %+v", rules)
	}

	// A provider resolved without an account is not given anyone's rules,
	// and no account sees another's helpers.
	plain := supervisor.NewRemoteProvider(supervisor.NewClient(f.server.SocketPath(), f.helper.StateDir()))
	if wrapped := f.runtime.wrapProvider(context.Background(), assetA, plain); wrapped != base.DurableProvider(plain) {
		t.Fatal("an ownerless provider was wrapped on a multi-account server")
	}
	for account, asset := range map[string]string{"rules-account-a": assetA, "rules-account-b": assetB} {
		status := f.runtime.status(account)
		if len(status) != 1 || status[0].AssetID != asset {
			t.Fatalf("status of %s = %+v", account, status)
		}
	}
}

func TestStateRuleHelperStatusNamesTheOutcome(t *testing.T) {
	runtime := newStateRulesRuntime(nil, nil, testStateRulesIdentity, false)
	h := runtime.helper("", ids.New())
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, stateRuleStatusApplied},
		{supervisor.ErrLimit, stateRuleStatusFailed},
		{supervisor.ErrInvalidInput, stateRuleStatusFailed},
		{base.ErrDisconnected, stateRuleStatusPending},
	} {
		h.StateRulesPushed("rev", tc.err)
		if got := runtime.status("")[0].Status; got != tc.want {
			t.Fatalf("status for %v = %s, want %s", tc.err, got, tc.want)
		}
	}
}
