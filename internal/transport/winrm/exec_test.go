package winrm

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"golang.org/x/text/encoding/simplifiedchinese"
)

type fakeRunner struct {
	run func(context.Context, string, io.Writer, io.Writer) (int, error)
}

func (f *fakeRunner) RunPowerShell(ctx context.Context, script string, stdout, stderr io.Writer) (int, error) {
	return f.run(ctx, script, stdout, stderr)
}

func testTransport(t *testing.T, runner powerShellRunner) *Transport {
	t.Helper()
	config, err := (Config{Host: "server.example", User: "alice"}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	transport, err := newWithRunner(config, runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	return transport
}

func writeResult(stdout, stderr io.Writer, body, cwd string) {
	_, _ = io.WriteString(stdout, body+"\n"+cwdSentinelHead+cwd+cwdSentinelTail+"\r\n")
}

func TestWrapAndUnwrapCWD(t *testing.T) {
	wrapped := wrapCommand(`C:\Users\O'Brien`, "Get-ChildItem")
	if !strings.Contains(wrapped, `Set-Location -LiteralPath 'C:\Users\O''Brien' -ErrorAction Stop`) {
		t.Fatalf("cwd is not safely quoted: %s", wrapped)
	}
	if strings.Index(wrapped, "$__nextermExit = $LASTEXITCODE") > strings.Index(wrapped, "[Console]::WriteLine") {
		t.Fatalf("exit status is captured after the sentinel: %s", wrapped)
	}
	stdout := "first\r\n\n" + cwdSentinelHead + `C:\Users\测试` + cwdSentinelTail + "\r\n"
	body, cwd, ok := unwrapCWD(stdout)
	if !ok || cwd != `C:\Users\测试` || body != "first\r\n" {
		t.Fatalf("unwrap = %q, %q, %v", body, cwd, ok)
	}
	for _, plain := range []string{"plain output", "output" + cwdSentinelHead + "incomplete", cwdSentinelHead + cwdSentinelTail} {
		body, _, ok = unwrapCWD(plain)
		if ok || body != plain {
			t.Fatalf("invalid sentinel changed output: %q, %v", body, ok)
		}
	}
}

func TestExecReturnsStatusDecodesStreamsAndFollowsCWD(t *testing.T) {
	gb18030, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte("错误输出"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	var mu sync.Mutex
	var scripts []string
	transport := testTransport(t, &fakeRunner{run: func(_ context.Context, script string, stdout, stderr io.Writer) (int, error) {
		mu.Lock()
		scripts = append(scripts, script)
		mu.Unlock()
		calls++
		writeResult(stdout, stderr, "标准输出", `D:\work`)
		_, _ = stderr.Write(gb18030)
		return 23, nil
	}})
	for range 2 {
		result, err := transport.Exec(context.Background(), "Write-Output test", base.ExecOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if result.ExitCode == nil || *result.ExitCode != 23 || result.Stdout != "标准输出" || result.Stderr != "错误输出" {
			t.Fatalf("unexpected result: %+v", result)
		}
		if result.Duration <= 0 || result.Truncated {
			t.Fatalf("unexpected duration or truncation: %+v", result)
		}
	}
	if transport.CWD() != `D:\work` || len(scripts) != 2 {
		t.Fatalf("cwd or calls not retained: %q, %d", transport.CWD(), len(scripts))
	}
	if !strings.Contains(scripts[0], `Set-Location -LiteralPath 'C:\'`) || !strings.Contains(scripts[1], `Set-Location -LiteralPath 'D:\work'`) {
		t.Fatalf("cwd not injected: %q", scripts)
	}
}

func TestExecTruncatesBodyWithoutLosingCWDSentinel(t *testing.T) {
	transport := testTransport(t, &fakeRunner{run: func(_ context.Context, _ string, stdout, stderr io.Writer) (int, error) {
		writeResult(stdout, stderr, "abcdefghij", `D:\after`)
		_, _ = io.WriteString(stderr, "stderr")
		return 0, nil
	}})
	result, err := transport.Exec(context.Background(), "command", base.ExecOptions{Limits: base.OutputLimits{Stdout: 4, Stderr: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "abcd" || result.Stderr != "std" || !result.Truncated {
		t.Fatalf("unexpected limited result: %+v", result)
	}
	if transport.CWD() != `D:\after` {
		t.Fatalf("sentinel was lost during truncation: %q", transport.CWD())
	}
}

func TestLimitTextDoesNotSplitUTF8(t *testing.T) {
	if got, truncated := limitText("中文", 4); got != "中" || !truncated {
		t.Fatalf("limitText = %q, %v", got, truncated)
	}
}

func TestExecTimeoutAndCancellation(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		transport := testTransport(t, &fakeRunner{run: func(ctx context.Context, _ string, _, _ io.Writer) (int, error) {
			<-ctx.Done()
			return 1, ctx.Err()
		}})
		result, err := transport.Exec(context.Background(), "wait", base.ExecOptions{Timeout: 20 * time.Millisecond})
		if !errors.Is(err, context.DeadlineExceeded) || result.ExitCode != nil || result.Duration <= 0 {
			t.Fatalf("timeout = %+v, %v", result, err)
		}
	})
	t.Run("caller cancellation", func(t *testing.T) {
		started := make(chan struct{})
		transport := testTransport(t, &fakeRunner{run: func(ctx context.Context, _ string, _, _ io.Writer) (int, error) {
			close(started)
			<-ctx.Done()
			return 1, ctx.Err()
		}})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := transport.Exec(ctx, "wait", base.ExecOptions{})
			done <- err
		}()
		<-started
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
	})
}

func TestExecCloseAndGeneration(t *testing.T) {
	started := make(chan struct{})
	transport := testTransport(t, &fakeRunner{run: func(ctx context.Context, _ string, _, _ io.Writer) (int, error) {
		close(started)
		<-ctx.Done()
		return 1, ctx.Err()
	}})
	if _, err := transport.Exec(context.Background(), "stale", base.ExecOptions{ExpectedGeneration: transport.Generation() + 1}); !errors.Is(err, base.ErrStaleGeneration) {
		t.Fatalf("stale generation = %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := transport.Exec(context.Background(), "wait", base.ExecOptions{})
		done <- err
	}()
	<-started
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, base.ErrClosed) {
		t.Fatalf("active close = %v", err)
	}
	if transport.IsAlive() {
		t.Fatal("closed transport is alive")
	}
	if _, err := transport.Exec(context.Background(), "closed", base.ExecOptions{}); !errors.Is(err, base.ErrClosed) {
		t.Fatalf("closed exec = %v", err)
	}
}

func TestExecCloseWhileWaitingForExecutionSlot(t *testing.T) {
	transport := testTransport(t, &fakeRunner{run: func(context.Context, string, io.Writer, io.Writer) (int, error) {
		t.Fatal("runner called while the execution slot is occupied")
		return 0, nil
	}})
	transport.execGate <- struct{}{}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := transport.Exec(context.Background(), "wait", base.ExecOptions{})
		done <- err
	}()
	<-started
	time.Sleep(10 * time.Millisecond)
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, base.ErrClosed) {
		t.Fatalf("waiting close = %v", err)
	}
}

func TestExecRejectsNegativeTimeout(t *testing.T) {
	called := false
	transport := testTransport(t, &fakeRunner{run: func(context.Context, string, io.Writer, io.Writer) (int, error) {
		called = true
		return 0, nil
	}})
	if _, err := transport.Exec(context.Background(), "command", base.ExecOptions{Timeout: -time.Second}); err == nil {
		t.Fatal("negative timeout succeeded")
	}
	if called {
		t.Fatal("runner called with negative timeout")
	}
}

func TestPingChecksRemoteExitStatus(t *testing.T) {
	transport := testTransport(t, &fakeRunner{run: func(_ context.Context, _ string, stdout, stderr io.Writer) (int, error) {
		writeResult(stdout, stderr, "", `C:\`)
		_, _ = io.WriteString(stderr, "denied")
		return 5, nil
	}})
	if _, err := transport.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("ping = %v", err)
	}
}
