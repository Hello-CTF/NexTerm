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

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
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

	requireCronError(t, dispatchCronTest(dispatcher, "cron_register",
		`{"sessionId":"missing","prompt":"p","schedule":"0 0 1 1 *"}`), "会话")

	var job cron.Job
	requireCronData(t, dispatchCronTest(dispatcher, "cron_register",
		`{"sessionId":"`+conversationA.ID+`","name":"nightly","prompt":"do the thing","schedule":"0 0 1 1 *","timezone":"UTC","timeoutMs":60000}`), &job)
	if job.SessionID != conversationA.ID || job.Name != "nightly" || job.Schedule != "0 0 1 1 *" ||
		job.Timezone != "UTC" || job.Timeout != time.Minute || !job.Enabled || job.NextRunAt.IsZero() {
		t.Fatalf("registered job = %+v", job)
	}

	requireCronError(t, dispatchCronTest(dispatcher, "cron_register",
		`{"sessionId":"`+conversationA.ID+`","prompt":"  ","schedule":"0 0 1 1 *"}`), "提示词")
	requireCronError(t, dispatchCronTest(dispatcher, "cron_register",
		`{"sessionId":"`+conversationA.ID+`","prompt":"p","schedule":"not-a-schedule"}`), "定时表达式")

	var listed []cron.Job
	requireCronData(t, dispatchCronTest(dispatcher, "cron_list", `{"sessionId":"`+conversationA.ID+`"}`), &listed)
	if len(listed) != 1 || listed[0].ID != job.ID {
		t.Fatalf("session A jobs = %+v", listed)
	}
	requireCronData(t, dispatchCronTest(dispatcher, "cron_list", `{"sessionId":"`+conversationB.ID+`"}`), &listed)
	if len(listed) != 0 {
		t.Fatalf("session B jobs = %+v", listed)
	}

	requireCronError(t, dispatchCronTest(dispatcher, "cron_get",
		`{"sessionId":"`+conversationB.ID+`","jobId":"`+job.ID+`"}`), "定时任务不存在")
	requireCronError(t, dispatchCronTest(dispatcher, "cron_set_enabled",
		`{"sessionId":"`+conversationB.ID+`","jobId":"`+job.ID+`","enabled":false}`), "定时任务不存在")
	requireCronError(t, dispatchCronTest(dispatcher, "cron_unregister",
		`{"sessionId":"`+conversationB.ID+`","jobId":"`+job.ID+`"}`), "定时任务不存在")

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

	response := dispatchCronTest(dispatcher, "cron_unregister", `{"sessionId":"`+conversationA.ID+`","jobId":"`+job.ID+`"}`)
	if !response.OK {
		t.Fatalf("unregister failed: %+v", response.Error)
	}
	requireCronData(t, dispatchCronTest(dispatcher, "cron_list", `{"sessionId":"`+conversationA.ID+`"}`), &listed)
	if len(listed) != 0 {
		t.Fatalf("jobs after unregister = %+v", listed)
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	requireCronError(t, dispatchCronTest(dispatcher, "cron_list", `{"sessionId":"`+conversationA.ID+`"}`), "定时任务存储失败")
}

func TestComposedCronRuntimeSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")

	openServices := func() (*ProductionServices, *store.Store) {
		database, err := store.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		initTestVault(t, ctx, database)
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
		if err := composeAIRuntime(ctx, services); err != nil {
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

func TestComposedCronUnattendedKeepsCustomDangerRules(t *testing.T) {
	ctx := context.Background()
	const command = "echo hi"
	services, database, transport, sessionID := composeOutcomeRuntime(t, command)
	if err := services.Guard.Set(ctx, guard.Config{Mode: guard.ReadWrite, DangerRules: []string{command}}); err != nil {
		t.Fatal(err)
	}

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

type blockingCronAgentRunner struct {
	started chan struct{}
	once    sync.Once
}

func (r *blockingCronAgentRunner) Start(context.Context, agent.ChatArgs, agent.StreamFactory) (agent.StartResponse, error) {
	r.once.Do(func() { close(r.started) })
	return agent.StartResponse{JobID: "cron-bounded-shutdown"}, nil
}

func (r *blockingCronAgentRunner) Cancel(string) error { return nil }

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
	executor := &cron.AgentExecutor{Runner: &blockingCronAgentRunner{started: started}}
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

func TestCronSchedulerFailureEmitsAppErrorEvent(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &ipcEventRecorder{}
	services := &ProductionServices{Store: database, Events: recorder}
	scheduler, _, err := composeCronScheduler(ctx, services, cron.Options{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &cronRuntime{scheduler: scheduler}
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runtime.Shutdown(shutdownCtx)
	})
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if payload, ok := recorder.appError(); ok {
			if payload.Code != "cron" || !strings.Contains(payload.Message, "sql: database is closed") {
				t.Fatalf("app error payload = %+v", payload)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("cron scheduler failure did not emit app://error")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestCronJobFailureDoesNotEmitAppErrorEvent(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	recorder := &ipcEventRecorder{}
	services := &ProductionServices{Store: database, Events: recorder}
	scheduler, _, err := composeCronScheduler(ctx, services, cron.Options{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &cronRuntime{scheduler: scheduler}
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runtime.Shutdown(shutdownCtx)
	})
	job, err := runtime.scheduler.Register(ctx, cron.Registration{SessionID: "session", Prompt: "fail", Schedule: "* * * * *"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().ExecContext(ctx,
		`UPDATE cron_job SET next_run_at = ? WHERE id = ?`,
		time.Now().Add(-time.Minute).UnixMilli(), job.ID); err != nil {
		t.Fatal(err)
	}
	finished := waitCronJobFinished(t, runtime.scheduler, "session", job.ID)
	if finished.LastError == "" || !strings.Contains(finished.LastError, "no runner") {
		t.Fatalf("job failure not recorded: %+v", finished)
	}
	if _, ok := recorder.appError(); ok {
		t.Fatal("ordinary job failure must not emit app://error")
	}
}
