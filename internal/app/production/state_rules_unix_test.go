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

type stateRuleFixture struct {
	runtime    *stateRulesRuntime
	dispatcher *ipc.Dispatcher
	helper     *supervisor.Supervisor
	provider   base.DurableProvider
	assetID    string
}

func newStateRuleFixture(t *testing.T) stateRuleFixture {
	t.Helper()
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runtime := newStateRulesRuntime(database, nil)
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
	assetID := ids.New()
	resolveCtx := ipc.WithHelperProgressScope(context.Background(), ipc.HelperProgressScope{AssetID: assetID})
	provider := runtime.wrapProvider(resolveCtx, assetID, supervisor.NewRemoteProvider(supervisor.NewClient(server.SocketPath(), helper.StateDir())))
	return stateRuleFixture{runtime: runtime, dispatcher: dispatcher, helper: helper, provider: provider, assetID: assetID}
}

func (f stateRuleFixture) dispatch(t *testing.T, command string, input any) ipc.Response {
	t.Helper()
	return dispatchFSTransferTest(f.dispatcher, context.Background(), command, input, nil)
}

// awaitRevision waits for the asynchronous push a rule change starts.
func (f stateRuleFixture) awaitRevision(t *testing.T, want func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		revision := f.helper.StateRulesRevision()
		if want(revision) {
			return revision
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper revision stayed %q", revision)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStateRuleChangesReachAnOnlineHelper(t *testing.T) {
	f := newStateRuleFixture(t)
	// Any helper operation registers the helper and syncs it.
	if _, err := f.provider.(*supervisor.RemoteProvider).ListDurable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := f.runtime.status(""); len(status) != 1 || status[0].Status != stateRuleStatusApplied || status[0].AssetID != f.assetID {
		t.Fatalf("status before rules = %+v", status)
	}

	hostRule := decodeFSTransferTest[stateRuleDTO](t, f.dispatch(t, "state_rule_save", stateRuleSaveRequest{
		Name: "review", Scope: store.StateRuleScopeHost, AssetID: f.assetID, Region: "bottom",
		Pattern: "Waiting for your review", State: "needs_me", Keys: "\r", Enabled: true,
	}))
	otherHost := decodeFSTransferTest[stateRuleDTO](t, f.dispatch(t, "state_rule_save", stateRuleSaveRequest{
		Scope: store.StateRuleScopeHost, AssetID: ids.New(), Region: "title", Pattern: "elsewhere", State: "idle", Enabled: true,
	}))
	revision := f.awaitRevision(t, func(revision string) bool { return revision != "" })
	rules := f.helper.StateRules().Rules
	if len(rules) != 1 || rules[0].ID != hostRule.ID || rules[0].Keys != "\r" || rules[0].Lines != 5 {
		t.Fatalf("helper rules = %+v (other host rule %s must not apply)", rules, otherHost.ID)
	}
	status := f.runtime.status("")
	if len(status) != 1 || status[0].Status != stateRuleStatusApplied || status[0].Revision != revision {
		t.Fatalf("status after push = %+v", status)
	}

	// An invalid rule is rejected visibly and the helper keeps its rules.
	response := f.dispatch(t, "state_rule_save", stateRuleSaveRequest{Scope: store.StateRuleScopeGlobal, Region: "bottom", Pattern: `(a)\1`, State: "idle", Enabled: true})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam || !strings.Contains(response.Error.Message, "匹配模式") {
		t.Fatalf("invalid rule response = %+v", response)
	}

	// Export and import round-trip; a disabled copy of the export does not
	// change what the helper runs, deleting the rule clears it.
	exported := decodeFSTransferTest[stateRuleExportDTO](t, f.dispatch(t, "state_rule_export", struct{}{}))
	if exported.Format != stateRuleExportFormat || len(exported.Rules) != 2 {
		t.Fatalf("export = %+v", exported)
	}
	result := decodeFSTransferTest[stateRuleImportResultDTO](t, f.dispatch(t, "state_rule_import", stateRuleImportRequest(exported)))
	if result != (stateRuleImportResultDTO{Unchanged: 2}) {
		t.Fatalf("re-import = %+v", result)
	}
	if response := f.dispatch(t, "state_rule_import", stateRuleImportRequest{Format: "other", Version: 1}); response.OK {
		t.Fatal("a foreign file was imported")
	}
	decodeFSTransferTest[stateRuleDeleteResult](t, f.dispatch(t, "state_rule_delete", stateRuleDeleteRequest{ID: hostRule.ID}))
	f.awaitRevision(t, func(revision string) bool { return revision == "" })
}
