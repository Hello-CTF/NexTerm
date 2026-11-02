package production

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/runbook"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// directiveStreamTransport is an SSH-like transport whose exec channel runs a
// Go function as the remote script, with a real stdin pipe.
type directiveStreamTransport struct {
	*execBatchTestTransport
	script func(stdout io.Writer, stdin *bufio.Reader)
}

type directiveStreamChannel struct {
	stdout *io.PipeReader
	stderr *io.PipeReader
	stdin  *io.PipeWriter
	done   chan struct{}
}

func (c *directiveStreamChannel) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *directiveStreamChannel) Stderr() io.Reader           { return c.stderr }
func (c *directiveStreamChannel) Write(p []byte) (int, error) { return c.stdin.Write(p) }
func (c *directiveStreamChannel) CloseWrite() error           { return c.stdin.Close() }
func (c *directiveStreamChannel) Close() error                { return c.stdin.Close() }
func (c *directiveStreamChannel) ID() string                  { return "directive" }
func (c *directiveStreamChannel) Generation() uint64          { return 1 }
func (c *directiveStreamChannel) Resize(context.Context, uint32, uint32) error {
	return nil
}
func (c *directiveStreamChannel) Wait(ctx context.Context) error {
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *directiveStreamTransport) OpenExec(ctx context.Context, _ string, _ base.ExecOptions) (base.Channel, error) {
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	stdinR, stdinW := io.Pipe()
	channel := &directiveStreamChannel{stdout: stdoutR, stderr: stderrR, stdin: stdinW, done: make(chan struct{})}
	go func() {
		defer close(channel.done)
		t.script(stdoutW, bufio.NewReader(stdinR))
		_ = stdoutW.Close()
		_ = stderrW.Close()
		_ = stdinR.Close()
	}()
	return channel, nil
}

func TestRunbookIPCScriptDirectivesEndToEnd(t *testing.T) {
	const secret = "tok-9f8e7d6c5b4a"
	gotSecret := make(chan string, 1)
	transport := &directiveStreamTransport{
		execBatchTestTransport: newExecBatchTestTransport(nil),
		script: func(stdout io.Writer, stdin *bufio.Reader) {
			fmt.Fprint(stdout, "@NEXTERM:STEP Configure\n@NEXTERM:PROGRESS 25\n@NEXTERM:INPUT region Region\teu\n")
			region, _ := stdin.ReadString('\n')
			fmt.Fprint(stdout, "@NEXTERM:SECRET token API token\n")
			token, _ := stdin.ReadString('\n')
			gotSecret <- strings.TrimSpace(token)
			fmt.Fprintf(stdout, "region=%s", region)
		},
	}
	manager := newExecBatchTestManager(t, transport)
	owner := ipc.WithUserID(t.Context(), "owner-a")
	other := ipc.WithUserID(t.Context(), "owner-b")
	connected, err := manager.ConnectFor(owner, session.Asset{ID: "asset-1", Name: "Web 1", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	auditor := &execBatchProductionAuditor{}
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	batches := newExecBatchService(manager, execBatchTestAssets(map[string]store.AssetRow{
		"asset-1": {ID: "asset-1", Name: "Web 1"},
	}), auditor, database)
	service := newRunbookIPCService(batches, database)
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}

	command := `sh -c 'printf "@NEXTERM:INPUT region Region\n"; printf "@NEXTERM:SECRET token API token\n"'`
	dispatchExecBatchTest(dispatcher, owner, "runbook_run_start", runbook.StartRequest{
		RunID: "run-d", RunbookID: "rb", RunbookName: "Directives",
		Steps:         []runbook.Step{{ID: "one", Name: "One", Command: command, Interactive: true}},
		SessionIDs:    []string{connected.ID},
		StopOnFailure: nil, ConfirmationPolicy: runbook.ConfirmProductionOnly,
	})
	dispatchExecBatchTest(dispatcher, owner, "runbook_run_prepare", runbook.PrepareRequest{
		RunID: "run-d", StepNumber: 1, Commands: []runbook.RenderedCommand{{SessionID: connected.ID, Command: command}},
	})
	advanced := make(chan runbook.RunState, 1)
	go func() {
		response := dispatchExecBatchTest(dispatcher, owner, "runbook_run_advance", runbook.AdvanceRequest{RunID: "run-d", StepNumber: 1, Confirmed: true})
		var state runbook.RunState
		_ = json.Unmarshal(response.Data, &state)
		advanced <- state
	}()

	waitForPrompt := func(kind runbook.PromptKind) runbook.Prompt {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			state := decodeExecBatchTest[runbook.RunState](t, dispatchExecBatchTest(dispatcher, owner, "runbook_run_get", runbook.RunRequest{RunID: "run-d"}))
			for _, prompt := range state.Prompts {
				if prompt.Kind == kind {
					return prompt
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("no %s prompt appeared", kind)
		return runbook.Prompt{}
	}

	region := waitForPrompt(runbook.PromptInput)
	if region.Text != "Region" || region.Default != "eu" || region.HostName == "" {
		t.Fatalf("prompt = %+v", region)
	}
	// Another user can neither see nor answer this run's prompts.
	if response := dispatchExecBatchTest(dispatcher, other, "runbook_run_get", runbook.RunRequest{RunID: "run-d"}); response.OK {
		t.Fatalf("other owner read the run: %+v", response)
	}
	denied := dispatchExecBatchTest(dispatcher, other, "runbook_run_respond", runbook.RespondRequest{RunID: "run-d", PromptID: region.ID, Value: "evil"})
	if denied.OK {
		t.Fatalf("other owner answered the prompt: %+v", denied)
	}
	dispatchExecBatchTest(dispatcher, owner, "runbook_run_respond", runbook.RespondRequest{RunID: "run-d", PromptID: region.ID, Value: "us-east"})
	token := waitForPrompt(runbook.PromptSecret)
	dispatchExecBatchTest(dispatcher, owner, "runbook_run_respond", runbook.RespondRequest{RunID: "run-d", PromptID: token.ID, Value: secret})

	var final runbook.RunState
	select {
	case final = <-advanced:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not finish")
	}
	if final.Status != runbook.RunSuccess {
		t.Fatalf("final = %+v", final)
	}
	if got := <-gotSecret; got != secret {
		t.Fatalf("script read secret %q", got)
	}
	result := final.Steps[0].Results[0]
	if result.Stdout != "region=us-east\n" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	if len(final.Live) != 1 || final.Live[0].SubStep != "Configure" || final.Live[0].Progress != 25 {
		t.Fatalf("live = %+v", final.Live)
	}
	if len(final.Answers) != 1 || final.Answers[0].Variable != "region" || final.Answers[0].Value != "us-east" {
		t.Fatalf("answers = %+v", final.Answers)
	}

	// The secret is in no run state, history, or audit payload.
	history := dispatchExecBatchTest(dispatcher, owner, "runbook_run_history_get", runbook.RunRequest{RunID: "run-d"})
	finalJSON, _ := json.Marshal(final)
	auditor.mu.Lock()
	auditJSON, _ := json.Marshal(auditor.records)
	inputAudits := 0
	for _, record := range auditor.records {
		if record.Kind == "exec_batch_input" {
			inputAudits++
		}
	}
	auditor.mu.Unlock()
	for name, blob := range map[string]string{"state": string(finalJSON), "history": string(history.Data), "audit": string(auditJSON)} {
		if strings.Contains(blob, secret) {
			t.Fatalf("secret leaked into %s: %s", name, blob)
		}
	}
	if inputAudits != 2 || !strings.Contains(string(auditJSON), `"variable":"token"`) {
		t.Fatalf("input audits = %d: %s", inputAudits, auditJSON)
	}
	if strings.Contains(string(auditJSON), "us-east") {
		t.Fatalf("answer values must not be audited: %s", auditJSON)
	}
}
