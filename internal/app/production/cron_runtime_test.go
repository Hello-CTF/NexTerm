package production

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/cron"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func dispatchCronTest(dispatcher *ipc.Dispatcher, command, args string) ipc.Response {
	return dispatcher.Dispatch(context.Background(), ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
}

func requireCronData(t *testing.T, response ipc.Response, target any) {
	t.Helper()
	if !response.OK {
		t.Fatalf("dispatch failed: %+v", response.Error)
	}
	if err := json.Unmarshal(response.Data, target); err != nil {
		t.Fatalf("decode %s: %v", response.Data, err)
	}
}

func requireCronError(t *testing.T, response ipc.Response, fragment string) {
	t.Helper()
	if response.OK {
		t.Fatalf("dispatch unexpectedly succeeded: %s", response.Data)
	}
	if response.Error == nil || !strings.Contains(response.Error.Message, fragment) {
		t.Fatalf("error = %+v, want message containing %q", response.Error, fragment)
	}
}

// TestComposedCronRuntimeCommandsAndSessionIsolation drives the composed cron
// RPC surface end to end: registration is bound to an existing conversation,
// lookups and mutations are scoped to the owning session, listing never
// deletes, unregister is the only deletion path, and storage failures surface
// honestly instead of collapsing into an empty result.
func TestComposedCronRuntimeCommandsAndSessionIsolation(t *testing.T) {
	ctx := context.Background()
	services, database, _, _ := composeOutcomeRuntime(t, "echo hi")
	if services.cron == nil {
		t.Fatal("composeAIRuntime did not compose the cron runtime")
	}
	dispatcher := ipc.NewDispatcher()
	if err := services.cron.registerCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"cron_register", "cron_list", "cron_get", "cron_set_enabled", "cron_unregister"} {
		if !slices.Contains(dispatcher.Commands(), command) {
			t.Fatalf("missing %s in %v", command, dispatcher.Commands())
		}
	}

	conversationA, err := database.ConvCreate(ctx, "cron-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	conversationB, err := database.ConvCreate(ctx, "cron-b", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Registration requires an existing conversation: the trigger's session ID
	// is executed as the conversation ID.
	requireCronError(t, dispatchCronTest(dispatcher, "cron_register",
		`{"sessionId":"missing","prompt":"p","schedule":"0 0 1 1 *"}`), "会话")

	var job cron.Job
	requireCronData(t, dispatchCronTest(dispatcher, "cron_register",
		`{"sessionId":"`+conversationA.ID+`","name":"nightly","prompt":"do the thing","schedule":"0 0 1 1 *","timezone":"UTC","timeoutMs":60000}`), &job)
	if job.SessionID != conversationA.ID || job.Name != "nightly" || job.Schedule != "0 0 1 1 *" ||
		job.Timezone != "UTC" || job.Timeout != time.Minute || !job.Enabled || job.NextRunAt.IsZero() {
		t.Fatalf("registered job = %+v", job)
	}

	// Validation failures surface honestly.
	requireCronError(t, dispatchCronTest(dispatcher, "cron_register",
		`{"sessionId":"`+conversationA.ID+`","prompt":"  ","schedule":"0 0 1 1 *"}`), "prompt")
	requireCronError(t, dispatchCronTest(dispatcher, "cron_register",
		`{"sessionId":"`+conversationA.ID+`","prompt":"p","schedule":"not-a-schedule"}`), "schedule")

	// Listing is read-only and session scoped; switching sessions never deletes.
	var listed []cron.Job
	requireCronData(t, dispatchCronTest(dispatcher, "cron_list", `{"sessionId":"`+conversationA.ID+`"}`), &listed)
	if len(listed) != 1 || listed[0].ID != job.ID {
		t.Fatalf("session A jobs = %+v", listed)
	}
	requireCronData(t, dispatchCronTest(dispatcher, "cron_list", `{"sessionId":"`+conversationB.ID+`"}`), &listed)
	if len(listed) != 0 {
		t.Fatalf("session B jobs = %+v", listed)
	}

	// Lookups and mutations from another session fail without touching the job.
	requireCronError(t, dispatchCronTest(dispatcher, "cron_get",
		`{"sessionId":"`+conversationB.ID+`","jobId":"`+job.ID+`"}`), "not found")
	requireCronError(t, dispatchCronTest(dispatcher, "cron_set_enabled",
		`{"sessionId":"`+conversationB.ID+`","jobId":"`+job.ID+`","enabled":false}`), "not found")
	requireCronError(t, dispatchCronTest(dispatcher, "cron_unregister",
		`{"sessionId":"`+conversationB.ID+`","jobId":"`+job.ID+`"}`), "not found")

	var disabled cron.Job
	requireCronData(t, dispatchCronTest(dispatcher, "cron_set_enabled",
		`{"sessionId":"`+conversationA.ID+`","jobId":"`+job.ID+`","enabled":false}`), &disabled)
	if disabled.Enabled {
		t.Fatalf("disabled job = %+v", disabled)
	}
	var enabled cron.Job
	requireCronData(t, dispatchCronTest(dispatcher, "cron_set_enabled",
		`{"sessionId":"`+conversationA.ID+`","jobId":"`+job.ID+`","enabled":true}`), &enabled)
	if !enabled.Enabled || enabled.NextRunAt.Before(time.Now()) {
		t.Fatalf("re-enabled job = %+v", enabled)
	}

	requireCronData(t, dispatchCronTest(dispatcher, "cron_list", `{"sessionId":"`+conversationA.ID+`"}`), &listed)
	if len(listed) != 1 || listed[0].ID != job.ID || !listed[0].Enabled {
		t.Fatalf("job after foreign-session attempts = %+v", listed)
	}

	// Unregister is the only deletion path.
	response := dispatchCronTest(dispatcher, "cron_unregister", `{"sessionId":"`+conversationA.ID+`","jobId":"`+job.ID+`"}`)
	if !response.OK {
		t.Fatalf("unregister failed: %+v", response.Error)
	}
	requireCronData(t, dispatchCronTest(dispatcher, "cron_list", `{"sessionId":"`+conversationA.ID+`"}`), &listed)
	if len(listed) != 0 {
		t.Fatalf("jobs after unregister = %+v", listed)
	}

	// Storage failures surface honestly instead of returning an empty list.
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	requireCronError(t, dispatchCronTest(dispatcher, "cron_list", `{"sessionId":"`+conversationA.ID+`"}`), "cron storage failure")
}

// TestComposedCronRuntimeSurvivesRestart proves the production lifecycle
// contract: jobs durable across a full close/reopen of the application store,
// no execution replayed, and every session's jobs intact — switching sessions
// only ever lists.
func TestComposedCronRuntimeSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")

	openServices := func() (*ProductionServices, *store.Store) {
		database, err := store.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		profileManager, err := profiles.NewManager(ctx, database)
		if err != nil {
			t.Fatal(err)
		}
		sessions := session.NewManager(session.Config{
			Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
				return &outcomeFakeTransport{}, nil
			}),
		})
		services := &ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions}
		if err := composeAIRuntime(ctx, services, "test-client"); err != nil {
			t.Fatal(err)
		}
		return services, database
	}

	first, firstDB := openServices()
	if first.cron == nil {
		t.Fatal("cron runtime not composed")
	}
	if err := first.cron.Start(ctx); err != nil {
		t.Fatal(err)
	}
	conversationA, err := firstDB.ConvCreate(ctx, "cron-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	conversationB, err := firstDB.ConvCreate(ctx, "cron-b", nil)
	if err != nil {
		t.Fatal(err)
	}
	jobA, err := first.cron.scheduler.Register(ctx, cron.Registration{SessionID: conversationA.ID, Name: "a", Prompt: "pa", Schedule: "0 0 1 1 *"})
	if err != nil {
		t.Fatal(err)
	}
	jobB, err := first.cron.scheduler.Register(ctx, cron.Registration{SessionID: conversationB.ID, Name: "b", Prompt: "pb", Schedule: "0 0 1 1 *"})
	if err != nil {
		t.Fatal(err)
	}

	// Session switching only lists: every session's jobs stay put.
	for range 3 {
		if jobs, err := first.cron.scheduler.List(ctx, conversationA.ID); err != nil || len(jobs) != 1 {
			t.Fatalf("session A jobs = %v, %v", jobs, err)
		}
		if jobs, err := first.cron.scheduler.List(ctx, conversationB.ID); err != nil || len(jobs) != 1 {
			t.Fatalf("session B jobs = %v, %v", jobs, err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := first.cron.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	first.closeAIRuntime()
	if err := first.Sessions.Close(); err != nil {
		t.Fatal(err)
	}
	if err := firstDB.Close(); err != nil {
		t.Fatal(err)
	}

	second, secondDB := openServices()
	defer func() {
		second.closeAIRuntime()
		_ = second.Sessions.Close()
		_ = secondDB.Close()
	}()
	reloadedA, err := second.cron.scheduler.List(ctx, conversationA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloadedA) != 1 || reloadedA[0].ID != jobA.ID {
		t.Fatalf("session A jobs after restart = %+v", reloadedA)
	}
	reloaded := reloadedA[0]
	if reloaded.Revision != jobA.Revision || !reloaded.Enabled ||
		reloaded.NextRunAt.UnixMilli() != jobA.NextRunAt.UnixMilli() ||
		reloaded.Running() || !reloaded.LastRunAt.IsZero() {
		t.Fatalf("job changed across restart: before %+v after %+v", jobA, reloaded)
	}
	reloadedB, err := second.cron.scheduler.List(ctx, conversationB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloadedB) != 1 || reloadedB[0].ID != jobB.ID {
		t.Fatalf("session B jobs after restart = %+v", reloadedB)
	}
}

// TestComposedCronUnattendedExecutionDeniesWithoutHITL drives the production
// composition end to end: a due cron job executes through the real agent
// runner with the unattended permission mode, and commands that would need a
// human confirmation — or are forbidden outright — are denied at decision
// time: never executed, never parked on a confirmation no one will answer.
func TestComposedCronUnattendedExecutionDeniesWithoutHITL(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		command string
	}{
		{name: "needs confirm", command: "systemctl restart nginx"},
		{name: "forbidden", command: "sudo -D /tmp rm -rf /home/user"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			services, database, transport, sessionID := composeOutcomeRuntime(t, testCase.command)
			conversation, err := database.ConvCreate(ctx, "cron target", map[string]any{"scope": tools.Scope{SessionID: sessionID}})
			if err != nil {
				t.Fatal(err)
			}
			job, err := services.cron.scheduler.Register(ctx, cron.Registration{
				SessionID: conversation.ID, Prompt: "run the maintenance command", Schedule: "* * * * *",
			})
			if err != nil {
				t.Fatal(err)
			}
			// Make the job due immediately: the composed scheduler polls real time.
			if _, err := database.DB().ExecContext(ctx,
				`UPDATE cron_job SET next_run_at = ? WHERE id = ?`,
				time.Now().Add(-time.Minute).UnixMilli(), job.ID); err != nil {
				t.Fatal(err)
			}
			if err := services.cron.Start(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = services.cron.Shutdown(shutdownCtx)
			})

			finished := waitCronJobFinished(t, services.cron.scheduler, conversation.ID, job.ID)
			if finished.LastError != "" || finished.ConsecutiveFailures != 0 {
				t.Fatalf("unattended run did not complete cleanly: %+v", finished)
			}
			if got := transport.effectCalls(testCase.command); got != 0 {
				t.Fatalf("denied command executed %d times: %v", got, transport.calls())
			}
		})
	}
}

// TestComposedCronInteractiveControlKeepsHITL pins the authority boundary the
// unattended mode guards: the same command interactively parks on a
// confirmation instead of being denied at decision time — and still has not
// executed anything.
func TestComposedCronInteractiveControlKeepsHITL(t *testing.T) {
	ctx := context.Background()
	const command = "systemctl restart nginx"
	services, _, transport, sessionID := composeOutcomeRuntime(t, command)

	stream := &agent.SliceStream{}
	response, err := services.Agent.Start(ctx, agent.ChatArgs{Message: "run it", Scope: tools.Scope{SessionID: sessionID}}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitOutcomeEvent(t, stream, "confirmRequired")
	if confirmation.ID != "call-exec-1" {
		t.Fatalf("confirmRequired = %+v", confirmation)
	}
	if got := transport.effectCalls(command); got != 0 {
		t.Fatalf("effect ran before confirmation: %d", got)
	}
	if err := services.Agent.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
}

// TestComposedCronUnattendedKeepsCustomDangerRules is the round-1 review
// regression: the unattended permission snapshot must preserve the user's
// custom danger rules. A built-in Safe command that matches a custom rule is
// upgraded to Danger at classification time and denied under cron — zero
// transport executions — while the interactive control still parks on a
// confirmation.
func TestComposedCronUnattendedKeepsCustomDangerRules(t *testing.T) {
	ctx := context.Background()
	const command = "echo hi"
	services, database, transport, sessionID := composeOutcomeRuntime(t, command)
	if err := services.Guard.Set(ctx, guard.Config{Mode: guard.ReadWrite, DangerRules: []string{command}}); err != nil {
		t.Fatal(err)
	}

	// Sanity: the custom rule upgrades the otherwise Safe command to Danger.
	permission, err := services.Guard.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(tools.ExecCommandsArgs{Commands: []string{command}})
	if err != nil {
		t.Fatal(err)
	}
	if ruling := guard.ClassifyTool("exec_commands", args, permission); ruling.Risk != guard.Danger {
		t.Fatalf("test setup: ruling = %+v, want Danger from the custom rule", ruling)
	}

	conversation, err := database.ConvCreate(ctx, "cron target", map[string]any{"scope": tools.Scope{SessionID: sessionID}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := services.cron.scheduler.Register(ctx, cron.Registration{
		SessionID: conversation.ID, Prompt: "run the maintenance command", Schedule: "* * * * *",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Make the job due immediately: the composed scheduler polls real time.
	if _, err := database.DB().ExecContext(ctx,
		`UPDATE cron_job SET next_run_at = ? WHERE id = ?`,
		time.Now().Add(-time.Minute).UnixMilli(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := services.cron.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = services.cron.Shutdown(shutdownCtx)
	})

	finished := waitCronJobFinished(t, services.cron.scheduler, conversation.ID, job.ID)
	if finished.LastError != "" || finished.ConsecutiveFailures != 0 {
		t.Fatalf("unattended run did not complete cleanly: %+v", finished)
	}
	if got := transport.effectCalls(command); got != 0 {
		t.Fatalf("custom-rule command executed %d times under cron: %v", got, transport.calls())
	}
}

// TestComposedCronInteractiveControlKeepsCustomDangerRules is the interactive
// half of the regression: with the same custom danger rule, the matched
// command parks on a confirmation instead of being denied at decision time.
func TestComposedCronInteractiveControlKeepsCustomDangerRules(t *testing.T) {
	ctx := context.Background()
	const command = "echo hi"
	services, _, transport, sessionID := composeOutcomeRuntime(t, command)
	if err := services.Guard.Set(ctx, guard.Config{Mode: guard.ReadWrite, DangerRules: []string{command}}); err != nil {
		t.Fatal(err)
	}

	stream := &agent.SliceStream{}
	response, err := services.Agent.Start(ctx, agent.ChatArgs{Message: "run it", Scope: tools.Scope{SessionID: sessionID}}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitOutcomeEvent(t, stream, "confirmRequired")
	if confirmation.ID != "call-exec-1" {
		t.Fatalf("confirmRequired = %+v", confirmation)
	}
	if got := transport.effectCalls(command); got != 0 {
		t.Fatalf("effect ran before confirmation: %d", got)
	}
	if err := services.Agent.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
}

// TestCronRuntimeBoundedShutdown proves the lifecycle contract: shutdown
// cancels in-flight executions through the executor, waits for the scheduler
// to drain within the caller's bound, and the interruption is recorded
// durably and honestly — never swallowed, never replayed.
func TestCronRuntimeBoundedShutdown(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	cronStore, err := cron.NewSQLiteStore(ctx, database.DB())
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	var once sync.Once
	executor := cron.ExecutorFunc(func(ctx context.Context, _ cron.Trigger) error {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return ctx.Err()
	})
	scheduler, err := cron.NewScheduler(cronStore, executor, cron.Options{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &cronRuntime{scheduler: scheduler}
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := scheduler.Register(ctx, cron.Registration{SessionID: "session", Prompt: "block", Schedule: "* * * * *"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().ExecContext(ctx,
		`UPDATE cron_job SET next_run_at = ? WHERE id = ?`,
		time.Now().Add(-time.Minute).UnixMilli(), job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for the cron execution to start")
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("bounded shutdown: %v", err)
	}
	// Idempotent: a second shutdown has nothing left to stop.
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}

	finished, err := scheduler.Get(ctx, "session", job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Running() || finished.LastError == "" || finished.ConsecutiveFailures != 1 {
		t.Fatalf("interrupted execution was not recorded honestly: %+v", finished)
	}
	if !strings.Contains(finished.LastError, "context canceled") {
		t.Fatalf("interruption reason = %q", finished.LastError)
	}
}

// TestProductionComposesCronModule verifies the production binary wiring: the
// cron RPC commands are registered on the application dispatcher and the
// scheduler component starts and shuts down with the application lifecycle.
func TestProductionComposesCronModule(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("production composition test requires a unix platform")
	}
	ctx := context.Background()
	production, err := NewProduction(ctx, ProductionConfig{
		Config: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, DataDir: t.TempDir(), Desktop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if production.Services.cron == nil {
		t.Fatal("production did not compose the cron runtime")
	}
	for _, command := range []string{"cron_register", "cron_list", "cron_get", "cron_set_enabled", "cron_unregister"} {
		if !slices.Contains(production.Dispatcher.Commands(), command) {
			t.Fatalf("production is missing %s", command)
		}
	}
	if err := production.Start(ctx); err != nil {
		t.Fatal(err)
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := production.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
}

func waitCronJobFinished(t *testing.T, scheduler *cron.Scheduler, sessionID, jobID string) cron.Job {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last cron.Job
	var lastErr error
	for {
		job, err := scheduler.Get(context.Background(), sessionID, jobID)
		if err == nil {
			last = job
			if !job.LastRunAt.IsZero() && !job.Running() {
				return job
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for cron job %s to finish (last: %+v, err: %v)", jobID, last, lastErr)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
