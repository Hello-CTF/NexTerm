package execbatch

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// scriptChannel is an exec channel whose remote side is a Go function: it
// prints to stdout, reads answers from stdin and exits.
type scriptChannel struct {
	stdout      *io.PipeReader
	stderr      *io.PipeReader
	stdinW      *io.PipeWriter
	done        chan struct{}
	exit        error
	closeWrites atomic.Int32
	stdinWrites atomic.Int32
}

func (c *scriptChannel) Read(p []byte) (int, error) { return c.stdout.Read(p) }
func (c *scriptChannel) Stderr() io.Reader          { return c.stderr }
func (c *scriptChannel) Write(p []byte) (int, error) {
	c.stdinWrites.Add(1)
	return c.stdinW.Write(p)
}
func (c *scriptChannel) CloseWrite() error {
	c.closeWrites.Add(1)
	return c.stdinW.Close()
}
func (c *scriptChannel) Resize(context.Context, uint32, uint32) error { return nil }
func (c *scriptChannel) ID() string                                   { return "script" }
func (c *scriptChannel) Generation() uint64                           { return 1 }
func (c *scriptChannel) Close() error                                 { _ = c.stdinW.Close(); return nil }
func (c *scriptChannel) Wait(ctx context.Context) error {
	select {
	case <-c.done:
		return c.exit
	case <-ctx.Done():
		return ctx.Err()
	}
}

type scriptEnv struct {
	stdout io.Writer
	stdin  *bufio.Reader
}

type scriptTransport struct {
	fakeStreamTransport
	kind    string
	run     func(ctx context.Context, env scriptEnv) error
	mu      sync.Mutex
	channel *scriptChannel
	options base.ExecOptions
}

func (t *scriptTransport) Kind() string { return t.kind }

func newScriptTransport(kind string, run func(ctx context.Context, env scriptEnv) error) *scriptTransport {
	t := &scriptTransport{kind: kind, run: run}
	t.open = func(ctx context.Context, command string, options base.ExecOptions) (base.Channel, error) {
		stdoutR, stdoutW := io.Pipe()
		stderrR, stderrW := io.Pipe()
		stdinR, stdinW := io.Pipe()
		channel := &scriptChannel{stdout: stdoutR, stderr: stderrR, stdinW: stdinW, done: make(chan struct{})}
		t.mu.Lock()
		t.channel = channel
		t.options = options
		t.mu.Unlock()
		go func() {
			defer close(channel.done)
			channel.exit = t.run(ctx, scriptEnv{stdout: stdoutW, stdin: bufio.NewReader(stdinR)})
			_ = stdoutW.Close()
			_ = stderrW.Close()
			_ = stdinR.Close()
		}()
		return channel, nil
	}
	return t
}

type testInteractor struct {
	mu     sync.Mutex
	events []Event
	ask    func(ctx context.Context, q Question) (Answer, error)
	asked  []Question
}

func (i *testInteractor) Notify(event Event) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.events = append(i.events, event)
}

func (i *testInteractor) Ask(ctx context.Context, q Question) (Answer, error) {
	i.mu.Lock()
	i.asked = append(i.asked, q)
	ask := i.ask
	i.mu.Unlock()
	if ask == nil {
		return Answer{}, ErrInputUnavailable
	}
	return ask(ctx, q)
}

func (i *testInteractor) snapshotEvents() []Event {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]Event(nil), i.events...)
}

type answerLog struct {
	mu      sync.Mutex
	records []AnswerRecord
}

func (l *answerLog) add(record AnswerRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, record)
}

func runInteractive(ctx context.Context, t *testing.T, transport base.Transport, command string, interactor Interactor, log *answerLog, timeout time.Duration) (Result, *ProgressTracker) {
	t.Helper()
	tracker := NewProgressTracker("batch", []string{"host-1"})
	target := Target{SessionID: "host-1", SessionName: "web-1", Transport: transport}
	options := TargetStreamOptions{Timeout: timeout, Interactor: interactor}
	if log != nil {
		options.OnAnswered = log.add
	}
	result, err := ExecuteTargetStreamCaptured(ctx, tracker, target, command, options)
	if err != nil {
		t.Fatal(err)
	}
	return result, tracker
}

const interactiveCommand = `sh -c 'printf "@NEXTERM:INPUT name Your name\n"; read n'`

func TestInteractiveInputRoundTrip(t *testing.T) {
	transport := newScriptTransport("ssh", func(ctx context.Context, env scriptEnv) error {
		fmt.Fprint(env.stdout, "start\n@NEXTERM:STEP Greeting\n@NEXTERM:PROGRESS 10\n@NEXTERM:INPUT name Your name\tworld\n")
		line, err := env.stdin.ReadString('\n')
		if err != nil {
			return err
		}
		fmt.Fprintf(env.stdout, "hello %s@NEXTERM:MESSAGE info done\n", line)
		return nil
	})
	interactor := &testInteractor{ask: func(_ context.Context, q Question) (Answer, error) {
		if q.Kind != DirectiveInput || q.Variable != "name" || q.Prompt != "Your name" || q.Default != "world" || q.SessionName != "web-1" {
			return Answer{}, fmt.Errorf("unexpected question %+v", q)
		}
		return Answer{Value: "alice"}, nil
	}}
	log := &answerLog{}
	result, _ := runInteractive(context.Background(), t, transport, interactiveCommand, interactor, log, 5*time.Second)
	if result.Status != StatusSuccess {
		t.Fatalf("result = %+v", result)
	}
	if result.Stdout != "start\nhello alice\n" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	events := interactor.snapshotEvents()
	if len(events) != 3 || events[0].Kind != DirectiveStep || events[0].Text != "Greeting" || events[1].Progress != 10 ||
		events[2].Kind != DirectiveMessage || events[2].SessionName != "web-1" {
		t.Fatalf("events = %+v", events)
	}
	if len(log.records) != 1 || log.records[0].Secret || log.records[0].Variable != "name" {
		t.Fatalf("answers = %+v", log.records)
	}
	if transport.channel.closeWrites.Load() != 0 {
		t.Fatalf("stdin was closed for an interactive command")
	}
}

func TestInteractiveSecretIsNeverRecorded(t *testing.T) {
	const secret = "s3cr3t-value-123"
	transport := newScriptTransport("ssh", func(ctx context.Context, env scriptEnv) error {
		fmt.Fprint(env.stdout, "@NEXTERM:SECRET token API token\n")
		line, _ := env.stdin.ReadString('\n')
		fmt.Fprintf(env.stdout, "got %d bytes\n", len(strings.TrimSpace(line)))
		return nil
	})
	interactor := &testInteractor{ask: func(context.Context, Question) (Answer, error) { return Answer{Value: secret}, nil }}
	log := &answerLog{}
	result, tracker := runInteractive(context.Background(), t, transport, `echo @NEXTERM:SECRET`, interactor, log, 5*time.Second)
	if result.Status != StatusSuccess || result.Stdout != fmt.Sprintf("got %d bytes\n", len(secret)) {
		t.Fatalf("result = %+v", result)
	}
	if len(log.records) != 1 || !log.records[0].Secret || log.records[0].Kind != DirectiveSecret {
		t.Fatalf("answers = %+v", log.records)
	}
	dump := fmt.Sprintf("%+v %+v %+v", result, log.records, interactor.snapshotEvents())
	_, targets := tracker.Snapshot()
	dump += fmt.Sprintf("%+v %+v", targets, tracker.Events(0))
	if strings.Contains(dump, secret) {
		t.Fatalf("secret leaked into results, progress or events")
	}
}

func TestInteractiveCommandWithoutInputMarkerKeepsStdinClosed(t *testing.T) {
	transport := newScriptTransport("ssh", func(ctx context.Context, env scriptEnv) error {
		fmt.Fprint(env.stdout, "@NEXTERM:INPUT name Your name\n")
		_, err := env.stdin.ReadString('\n')
		fmt.Fprintf(env.stdout, "stdin=%v\n", err)
		return nil
	})
	interactor := &testInteractor{}
	result, _ := runInteractive(context.Background(), t, transport, `./run.sh`, interactor, nil, 5*time.Second)
	if result.Status != StatusSuccess || result.Stdout != "stdin=EOF\n" {
		t.Fatalf("result = %+v", result)
	}
	if transport.channel.closeWrites.Load() != 1 || transport.channel.stdinWrites.Load() != 0 {
		t.Fatalf("stdin must close immediately as before")
	}
	if len(interactor.asked) != 0 {
		t.Fatalf("a prompt was raised without stdin")
	}
	events := interactor.snapshotEvents()
	if len(events) != 1 || events[0].Level != LevelWarn || !strings.Contains(events[0].Text, "标准输入已关闭") {
		t.Fatalf("events = %+v", events)
	}
}

func TestNoInteractorLeavesOutputAndStdinUntouched(t *testing.T) {
	transport := newScriptTransport("ssh", func(ctx context.Context, env scriptEnv) error {
		fmt.Fprint(env.stdout, "@NEXTERM:INPUT name Your name\n")
		_, err := env.stdin.ReadString('\n')
		fmt.Fprintf(env.stdout, "stdin=%v\n", err)
		return nil
	})
	result, _ := runInteractive(context.Background(), t, transport, interactiveCommand, nil, nil, 5*time.Second)
	if result.Stdout != "@NEXTERM:INPUT name Your name\nstdin=EOF\n" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	if transport.channel.closeWrites.Load() != 1 {
		t.Fatalf("stdin must close immediately")
	}
	if transport.options.Timeout != 5*time.Second {
		t.Fatalf("transport timeout = %v", transport.options.Timeout)
	}
}

func TestInteractiveMalformedDirectiveIsVisible(t *testing.T) {
	transport := newScriptTransport("ssh", func(ctx context.Context, env scriptEnv) error {
		fmt.Fprint(env.stdout, "@NEXTERM:PROGRESS lots\nplain\n")
		return nil
	})
	interactor := &testInteractor{}
	result, _ := runInteractive(context.Background(), t, transport, "run", interactor, nil, 5*time.Second)
	if result.Status != StatusSuccess || result.Stdout != "@NEXTERM:PROGRESS lots\nplain\n" {
		t.Fatalf("result = %+v", result)
	}
	events := interactor.snapshotEvents()
	if len(events) != 1 || events[0].Level != LevelWarn || !strings.Contains(events[0].Text, "格式错误") {
		t.Fatalf("events = %+v", events)
	}
}

func TestInteractiveInputTimeoutFailsTargetVisibly(t *testing.T) {
	transport := newScriptTransport("ssh", func(ctx context.Context, env scriptEnv) error {
		fmt.Fprint(env.stdout, "@NEXTERM:INPUT name Your name\n")
		_, _ = env.stdin.ReadString('\n')
		return nil
	})
	interactor := &testInteractor{ask: func(context.Context, Question) (Answer, error) { return Answer{}, ErrInputTimeout }}
	result, _ := runInteractive(context.Background(), t, transport, interactiveCommand, interactor, nil, 5*time.Second)
	if result.Status != StatusTransportError || !strings.Contains(result.Error, ErrInputTimeout.Error()) {
		t.Fatalf("result = %+v", result)
	}
}

func TestInteractiveCancelWhilePromptPending(t *testing.T) {
	transport := newScriptTransport("ssh", func(ctx context.Context, env scriptEnv) error {
		fmt.Fprint(env.stdout, "@NEXTERM:INPUT name Your name\n")
		_, _ = env.stdin.ReadString('\n')
		return nil
	})
	asking := make(chan struct{})
	interactor := &testInteractor{ask: func(ctx context.Context, _ Question) (Answer, error) {
		close(asking)
		<-ctx.Done()
		return Answer{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-asking
		cancel()
	}()
	result, _ := runInteractive(ctx, t, transport, interactiveCommand, interactor, nil, 5*time.Second)
	if result.Status != StatusCanceled {
		t.Fatalf("result = %+v", result)
	}
}

func TestInteractiveDisconnectWhilePromptPendingUnblocks(t *testing.T) {
	// The remote side ends (connection lost) while the operator is still being asked.
	transport := newScriptTransport("ssh", func(ctx context.Context, env scriptEnv) error {
		fmt.Fprint(env.stdout, "@NEXTERM:INPUT name Your name\n")
		time.Sleep(50 * time.Millisecond)
		return errors.New("connection lost")
	})
	interactor := &testInteractor{ask: func(ctx context.Context, _ Question) (Answer, error) {
		<-ctx.Done()
		return Answer{}, ctx.Err()
	}}
	done := make(chan Result, 1)
	go func() {
		result, _ := runInteractive(context.Background(), t, transport, interactiveCommand, interactor, nil, 5*time.Second)
		done <- result
	}()
	select {
	case result := <-done:
		if result.Status != StatusTransportError || !strings.Contains(result.Error, "connection lost") {
			t.Fatalf("result = %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("target stayed blocked on a prompt after the connection ended")
	}
}

func TestInteractiveWaitingForOperatorDoesNotConsumeExecTimeout(t *testing.T) {
	transport := newScriptTransport("ssh", func(ctx context.Context, env scriptEnv) error {
		fmt.Fprint(env.stdout, "@NEXTERM:INPUT name Your name\n")
		_, _ = env.stdin.ReadString('\n')
		fmt.Fprint(env.stdout, "ok\n")
		return nil
	})
	interactor := &testInteractor{ask: func(ctx context.Context, _ Question) (Answer, error) {
		time.Sleep(300 * time.Millisecond)
		return Answer{Value: "x"}, nil
	}}
	result, _ := runInteractive(context.Background(), t, transport, interactiveCommand, interactor, nil, 100*time.Millisecond)
	if result.Status != StatusSuccess || result.Stdout != "ok\n" {
		t.Fatalf("result = %+v", result)
	}
	if transport.options.Timeout != 0 {
		t.Fatalf("interactive exec must not use the transport timeout, got %v", transport.options.Timeout)
	}
}

func TestInteractiveScriptStillTimesOutWhenNotWaitingForOperator(t *testing.T) {
	transport := newScriptTransport("ssh", func(ctx context.Context, env scriptEnv) error {
		<-ctx.Done()
		return ctx.Err()
	})
	result, _ := runInteractive(context.Background(), t, transport, interactiveCommand, &testInteractor{}, nil, 100*time.Millisecond)
	if result.Status != StatusTimeout {
		t.Fatalf("result = %+v", result)
	}
}

func TestInteractiveUnsupportedTransportsReportVisibly(t *testing.T) {
	t.Run("device exec channel without stdin", func(t *testing.T) {
		transport := newScriptTransport("device", func(ctx context.Context, env scriptEnv) error {
			fmt.Fprint(env.stdout, "@NEXTERM:CONFIRM Proceed?\n@NEXTERM:STEP Working\ndone\n")
			return nil
		})
		interactor := &testInteractor{}
		result, _ := runInteractive(context.Background(), t, transport, `echo @NEXTERM:CONFIRM`, interactor, nil, 5*time.Second)
		if result.Status != StatusSuccess || result.Stdout != "done\n" || len(interactor.asked) != 0 {
			t.Fatalf("result = %+v asked=%v", result, interactor.asked)
		}
		events := interactor.snapshotEvents()
		if len(events) != 2 || events[0].Level != LevelWarn || !strings.Contains(events[0].Text, "device") || events[1].Kind != DirectiveStep {
			t.Fatalf("events = %+v", events)
		}
	})
	t.Run("one-shot exec transport", func(t *testing.T) {
		transport := &directiveOneShotTransport{stdout: "@NEXTERM:STEP A\n@NEXTERM:SECRET pw Password\nresult\n"}
		interactor := &testInteractor{}
		result, _ := runInteractive(context.Background(), t, transport, `echo @NEXTERM:SECRET`, interactor, nil, 5*time.Second)
		if result.Status != StatusSuccess || result.Stdout != "result\n" || len(interactor.asked) != 0 {
			t.Fatalf("result = %+v", result)
		}
		events := interactor.snapshotEvents()
		if len(events) != 2 || events[1].Level != LevelWarn || !strings.Contains(events[1].Text, "local") {
			t.Fatalf("events = %+v", events)
		}
	})
}

type directiveOneShotTransport struct {
	stdout string
}

func (t *directiveOneShotTransport) Kind() string                                { return "local" }
func (t *directiveOneShotTransport) Generation() uint64                          { return 0 }
func (t *directiveOneShotTransport) IsAlive() bool                               { return true }
func (t *directiveOneShotTransport) Close() error                                { return nil }
func (t *directiveOneShotTransport) Ping(context.Context) (time.Duration, error) { return 0, nil }
func (t *directiveOneShotTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	code := 0
	return base.ExecResult{Stdout: t.stdout, ExitCode: &code}, nil
}

func TestInteractiveDirectiveFloodIsRateLimited(t *testing.T) {
	const lines = 5000
	transport := newScriptTransport("ssh", func(ctx context.Context, env scriptEnv) error {
		var flood strings.Builder
		for range lines {
			flood.WriteString("@NEXTERM:MESSAGE info spam\n")
		}
		fmt.Fprint(env.stdout, flood.String(), "tail\n")
		return nil
	})
	interactor := &testInteractor{}
	result, _ := runInteractive(context.Background(), t, transport, "run", interactor, nil, 5*time.Second)
	if result.Status != StatusSuccess || result.Stdout != "tail\n" {
		t.Fatalf("result = %+v", result)
	}
	events := interactor.snapshotEvents()
	if len(events) > directiveBurst+directiveRefillPerSecond*5+2 {
		t.Fatalf("%d events leaked past the rate limit", len(events))
	}
	warnings := 0
	for _, event := range events {
		if event.Level == LevelWarn {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("expected exactly one rate limit warning, got %d", warnings)
	}
}

func TestInteractivePromptCountLimit(t *testing.T) {
	transport := newScriptTransport("ssh", func(ctx context.Context, env scriptEnv) error {
		for range maxPromptsPerTarget + 5 {
			fmt.Fprint(env.stdout, "@NEXTERM:INPUT v Again\n")
			if _, err := env.stdin.ReadString('\n'); err != nil {
				return err
			}
			time.Sleep(60 * time.Millisecond) // stay below the directive rate limit
		}
		return nil
	})
	var count atomic.Int32
	interactor := &testInteractor{ask: func(context.Context, Question) (Answer, error) {
		count.Add(1)
		return Answer{Value: "x"}, nil
	}}
	result, _ := runInteractive(context.Background(), t, transport, interactiveCommand, interactor, nil, 30*time.Second)
	if result.Status != StatusTransportError || count.Load() != maxPromptsPerTarget {
		t.Fatalf("result = %+v asked=%d", result, count.Load())
	}
}

func TestInteractiveRejectsMultiLineAnswer(t *testing.T) {
	transport := newScriptTransport("ssh", func(ctx context.Context, env scriptEnv) error {
		fmt.Fprint(env.stdout, "@NEXTERM:INPUT v Again\n")
		_, _ = env.stdin.ReadString('\n')
		return nil
	})
	interactor := &testInteractor{ask: func(context.Context, Question) (Answer, error) { return Answer{Value: "a\nrm -rf /"}, nil }}
	result, _ := runInteractive(context.Background(), t, transport, interactiveCommand, interactor, nil, 5*time.Second)
	if result.Status != StatusTransportError || transport.channel.stdinWrites.Load() != 0 {
		t.Fatalf("result = %+v writes=%d", result, transport.channel.stdinWrites.Load())
	}
}

func TestInteractivePerHostPrompts(t *testing.T) {
	// Each host's script is asked separately; one host's answer is never reused for another.
	var calls sync.Map
	transport := func(host string) *scriptTransport {
		return newScriptTransport("ssh", func(ctx context.Context, env scriptEnv) error {
			fmt.Fprint(env.stdout, "@NEXTERM:INPUT v Value\n")
			line, _ := env.stdin.ReadString('\n')
			fmt.Fprintf(env.stdout, "%s got %s", host, line)
			return nil
		})
	}
	interactor := &testInteractor{ask: func(_ context.Context, q Question) (Answer, error) {
		calls.Store(q.SessionID, q.SessionName)
		return Answer{Value: "for-" + q.SessionID}, nil
	}}
	var wg sync.WaitGroup
	results := make([]Result, 3)
	for index := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			host := fmt.Sprintf("h%d", index)
			tracker := NewProgressTracker("b", []string{host})
			result, err := ExecuteTargetStreamCaptured(context.Background(), tracker,
				Target{SessionID: host, SessionName: "name-" + host, Transport: transport(host)},
				interactiveCommand, TargetStreamOptions{Timeout: 5 * time.Second, Interactor: interactor})
			if err != nil {
				t.Error(err)
			}
			results[index] = result
		}()
	}
	wg.Wait()
	for index, result := range results {
		host := fmt.Sprintf("h%d", index)
		if want := fmt.Sprintf("%s got for-%s\n", host, host); result.Stdout != want {
			t.Fatalf("host %s stdout = %q, want %q", host, result.Stdout, want)
		}
		if name, _ := calls.Load(host); name != "name-"+host {
			t.Fatalf("host %s asked with name %v", host, name)
		}
	}
}
