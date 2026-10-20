package production

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestCronExecutionContextOnlyOpensExplicitSharedCalls(t *testing.T) {
	account := ipc.WithUserID(context.Background(), "owner-a")
	if got := cronExecutionContext(account, false); got != account || owner.OpenAccess(got) {
		t.Fatal("account cron context gained Open access")
	}
	if got := cronExecutionContext(account, true); got != account || owner.OpenAccess(got) {
		t.Fatal("account cron context gained Open access in open deployment")
	}
	if got := cronExecutionContext(context.Background(), true); !owner.OpenAccess(got) {
		t.Fatal("explicit Open cron context lost Open access")
	}
	if got := cronExecutionContext(context.Background(), false); owner.OpenAccess(got) {
		t.Fatal("unknown account-mode cron context gained Open access")
	}
}

func TestComposeCronSchedulerDistinguishesOpenLegacyCapability(t *testing.T) {
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	services := &ProductionServices{Store: database}
	_, accountExecutor, err := composeCronScheduler(context.Background(), services, cron.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if accountExecutor.AllowOwnerlessLegacy {
		t.Fatal("account mode enabled ownerless legacy execution")
	}
	_, openExecutor, err := composeCronScheduler(owner.WithOpenAccess(context.Background()), services, cron.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !openExecutor.AllowOwnerlessLegacy {
		t.Fatal("explicit Open deployment disabled ownerless legacy execution")
	}
}

func TestCronRegisterRejectsInvalidScopeBeforeCreate(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	conversation, err := database.ConvCreate(ctx, "owned", map[string]any{"scope": tools.Scope{SessionID: "session-a"}})
	if err != nil {
		t.Fatal(err)
	}
	cronStore, err := cron.NewSQLiteStore(context.Background(), database.GORM(), cron.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := cron.NewScheduler(cronStore, &cron.AgentExecutor{Runner: &blockingCronAgentRunner{started: make(chan struct{})}}, cron.Options{})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &cronRuntime{
		scheduler: scheduler, conversations: database,
		scopeValidator: agent.ScopeValidatorFunc(func(context.Context, tools.Scope) error {
			return errors.New("invalid scope")
		}),
	}
	dispatcher := ipc.NewDispatcher()
	if err := runtime.registerCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	response := dispatcher.Dispatch(ctx, ipc.Request{Command: "cron_register", Args: json.RawMessage(`{"sessionId":"` + conversation.ID + `","prompt":"run","schedule":"* * * * *"}`)}, ipc.Environment{})
	if response.OK {
		t.Fatal("invalid scope cron registration succeeded")
	}
	jobs, err := scheduler.List(ctx, conversation.ID)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("rejected registration persisted jobs: %+v, %v", jobs, err)
	}
}
