package takeover

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/cloudwego/eino/components/model"
)

// newServerHarness wires the manager like the multi-user server deployment:
// Shared is false, so a caller without an account identity must be rejected
// before any side effect instead of acting as an empty owner.
func newServerHarness(t *testing.T, configure func(*Dependencies)) *harness {
	t.Helper()
	return newHarnessWith(t, sequence(toolCallMessage(doneCall("finished"))), func(deps *Dependencies) {
		deps.Shared = false
		if configure != nil {
			configure(deps)
		}
	})
}

func requireIdentityLessForbidden(t *testing.T, response ipc.Response, command string) {
	t.Helper()
	if response.OK {
		t.Fatalf("identity-less %s succeeded: %+v", command, response)
	}
	if response.Error == nil || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("identity-less %s error = %+v, want forbidden", command, response.Error)
	}
}

func takeoverOwnerships(h *harness) map[string]string {
	h.manager.mu.Lock()
	defer h.manager.mu.Unlock()
	ids := make(map[string]string, len(h.manager.owners))
	for tabID, own := range h.manager.owners {
		ids[tabID] = own.ownerID
	}
	return ids
}

func (h *harness) bannerCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.banners)
}

func registerTakeoverDispatcher(t *testing.T, h *harness) *ipc.Dispatcher {
	t.Helper()
	dispatcher := ipc.NewDispatcher()
	if err := h.manager.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func TestIdentityLessEnterFailsClosedWithoutOwnershipOrBanner(t *testing.T) {
	h := newServerHarness(t, nil)
	dispatcher := registerTakeoverDispatcher(t, h)

	_, err := h.manager.Enter(context.Background(), "tab")
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeForbidden {
		t.Fatalf("identity-less manager enter error = %v, want forbidden", err)
	}
	requireIdentityLessForbidden(t, dispatchTakeover(t, dispatcher, context.Background(), "ai_takeover_enter", map[string]any{"tabId": "tab"}, ipc.Environment{}), "ai_takeover_enter")
	if owners := takeoverOwnerships(h); len(owners) != 0 {
		t.Fatalf("identity-less enter recorded ownership: %+v", owners)
	}
	if banners := h.bannerCount(); banners != 0 {
		t.Fatalf("identity-less enter injected %d banners", banners)
	}

	alice := ipc.WithUserID(context.Background(), "account-a")
	token := enterTakeoverIPC(t, dispatcher, alice, "tab")
	if token == "" {
		t.Fatal("legitimate owner enter failed after identity-less rejection")
	}
	if owners := takeoverOwnerships(h); owners["tab"] != "account-a" {
		t.Fatalf("ownership after legitimate enter = %+v", owners)
	}
}

func TestIdentityLessControlFailsClosedAfterLegitimateEnter(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)),
		toolCallMessage(doneCall("finished")),
	)
	h := newServerHarness(t, func(deps *Dependencies) {
		deps.Model = func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil }
	})
	dispatcher := registerTakeoverDispatcher(t, h)
	alice := ipc.WithUserID(context.Background(), "account-a")
	token := enterTakeoverIPC(t, dispatcher, alice, "tab")
	stream := &agent.SliceStream{}
	run := runTakeoverIPC(t, dispatcher, alice, RunArgs{TabID: "tab", Token: token, Instruction: "go"}, stream)
	confirmation := waitEvent(t, stream, "confirmRequired")

	gateway := context.Background()
	requireIdentityLessForbidden(t, dispatchTakeover(t, dispatcher, gateway, "ai_takeover_enter", map[string]any{"tabId": "tab"}, ipc.Environment{}), "ai_takeover_enter")
	requireIdentityLessForbidden(t, dispatchTakeover(t, dispatcher, gateway, "ai_takeover_run", RunArgs{TabID: "tab", Instruction: "seize"}, takeoverStreamEnvironment(&agent.SliceStream{})), "ai_takeover_run")
	requireIdentityLessForbidden(t, dispatchTakeover(t, dispatcher, gateway, "ai_takeover_resume", ResumeArgs{TabID: "tab", Token: token, JobID: run.JobID}, takeoverStreamEnvironment(&agent.SliceStream{})), "ai_takeover_resume")
	requireIdentityLessForbidden(t, dispatchTakeover(t, dispatcher, gateway, "ai_takeover_confirm", agent.Confirmation{JobID: run.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "deny"}, ipc.Environment{}), "ai_takeover_confirm")
	requireIdentityLessForbidden(t, dispatchTakeover(t, dispatcher, gateway, "ai_takeover_cancel", map[string]any{"jobId": run.JobID}, ipc.Environment{}), "ai_takeover_cancel")
	requireIdentityLessForbidden(t, dispatchTakeover(t, dispatcher, gateway, "ai_takeover_pause", map[string]any{"tabId": "tab", "token": token}, ipc.Environment{}), "ai_takeover_pause")
	requireIdentityLessForbidden(t, dispatchTakeover(t, dispatcher, gateway, "ai_takeover_exit", map[string]any{"tabId": "tab", "token": token, "reason": "seize"}, ipc.Environment{}), "ai_takeover_exit")

	if owners := takeoverOwnerships(h); owners["tab"] != "account-a" {
		t.Fatalf("identity-less control displaced the legitimate owner: %+v", owners)
	}
	confirm := agent.Confirmation{JobID: run.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}
	if response := dispatchTakeover(t, dispatcher, alice, "ai_takeover_confirm", confirm, ipc.Environment{}); !response.OK {
		t.Fatalf("legitimate owner confirm after identity-less attempts: %+v", response)
	}
	events := waitClosed(t, stream)
	if done, failed := counts(events); done != 1 || failed != 0 {
		t.Fatalf("events = %+v", events)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 {
		t.Fatalf("writes = %q", h.aiWrites)
	}
}

func TestCrossAccountEnterDoesNotDisplaceOwner(t *testing.T) {
	h := newServerHarness(t, nil)
	dispatcher := registerTakeoverDispatcher(t, h)
	alice := ipc.WithUserID(context.Background(), "account-a")
	bob := ipc.WithUserID(context.Background(), "account-b")
	token := enterTakeoverIPC(t, dispatcher, alice, "tab")

	response := dispatchTakeover(t, dispatcher, bob, "ai_takeover_enter", map[string]any{"tabId": "tab"}, ipc.Environment{})
	if response.OK {
		t.Fatal("cross-account enter displaced the owner")
	}
	if owners := takeoverOwnerships(h); owners["tab"] != "account-a" {
		t.Fatalf("ownership after cross-account attempt = %+v", owners)
	}
	stream := &agent.SliceStream{}
	runTakeoverIPC(t, dispatcher, alice, RunArgs{TabID: "tab", Token: token, Instruction: "go"}, stream)
	events := waitClosed(t, stream)
	if done, failed := counts(events); done != 1 || failed != 0 {
		t.Fatalf("owner run after cross-account attempt: %+v", events)
	}
}

func TestSharedDeploymentAllowsOwnerlessEnter(t *testing.T) {
	h := newHarness(t, sequence(toolCallMessage(doneCall("finished"))))
	dispatcher := registerTakeoverDispatcher(t, h)
	token := enterTakeoverIPC(t, dispatcher, context.Background(), "tab")
	if token == "" {
		t.Fatal("shared-mode ownerless enter failed")
	}
	owners := takeoverOwnerships(h)
	if len(owners) != 1 || owners["tab"] != "" {
		t.Fatalf("shared-mode ownership = %+v, want single empty owner", owners)
	}
	h.mu.Lock()
	banners := strings.Join(h.banners, "\n")
	h.mu.Unlock()
	if !strings.Contains(banners, "AI 正在操作") {
		t.Fatalf("shared-mode enter did not inject the banner: %q", banners)
	}
}

func TestOpenAccessContextAllowsOwnerlessEnter(t *testing.T) {
	h := newServerHarness(t, nil)
	dispatcher := registerTakeoverDispatcher(t, h)
	open := owner.WithOpenAccess(context.Background())
	token := enterTakeoverIPC(t, dispatcher, open, "tab")
	if token == "" {
		t.Fatal("open-access ownerless enter failed")
	}
	if owners := takeoverOwnerships(h); len(owners) != 1 {
		t.Fatalf("open-access ownership = %+v", owners)
	}
}

func TestIdentityLessEnterRaceFailsClosed(t *testing.T) {
	h := newServerHarness(t, nil)
	dispatcher := registerTakeoverDispatcher(t, h)
	alice := ipc.WithUserID(context.Background(), "account-a")

	const racers = 8
	begin := make(chan struct{})
	var wg sync.WaitGroup
	anonymous := make(chan ipc.Response, racers)
	ownerResult := make(chan ipc.Response, 1)
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin
			anonymous <- dispatchTakeover(t, dispatcher, context.Background(), "ai_takeover_enter", map[string]any{"tabId": "tab"}, ipc.Environment{})
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-begin
		ownerResult <- dispatchTakeover(t, dispatcher, alice, "ai_takeover_enter", map[string]any{"tabId": "tab"}, ipc.Environment{})
	}()
	close(begin)
	wg.Wait()
	close(anonymous)

	for response := range anonymous {
		requireIdentityLessForbidden(t, response, "ai_takeover_enter")
	}
	if response := <-ownerResult; !response.OK {
		t.Fatalf("legitimate owner lost the enter race to identity-less callers: %+v", response)
	}
	if owners := takeoverOwnerships(h); len(owners) != 1 || owners["tab"] != "account-a" {
		t.Fatalf("ownership after race = %+v", owners)
	}
}
