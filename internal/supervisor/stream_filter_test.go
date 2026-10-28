package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const staleDigest = "0123456789abcdef"

func streamFakeShell(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func streamDefaults(shell, path string) CreateDefaults {
	return CreateDefaults{
		Shell: func(request string) (string, bool) {
			if request != "" {
				return request, true
			}
			return shell, true
		},
		Path: func() string { return path },
	}
}

func createFrame(t *testing.T, msg map[string]any) []byte {
	t.Helper()
	payload, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	return appendFrame(nil, frameCreate, payload)
}

// readCreate decodes the single create frame of a stream.
func readCreate(t *testing.T, stream []byte) createMsg {
	t.Helper()
	kind, payload, err := readFrame(bytes.NewReader(stream))
	if err != nil || kind != frameCreate {
		t.Fatalf("frame = %v, %v; want a create frame", kind, err)
	}
	var msg createMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

// helperStartedBadly simulates what a helper started earlier does with a create request: the behavior of
// every helper build since protocol 2. It starts the shell named by a SHELL in the request, else its own
// SHELL, and runs it with its own environment overlaid with the request's.
func helperStartedBadly(request createMsg, helperEnv []string) (command []string, env []string) {
	env = append([]string(nil), helperEnv...)
	for _, entry := range request.Env {
		key, _, _ := strings.Cut(entry, "=")
		replaced := false
		for index, existing := range env {
			if existingKey, _, _ := strings.Cut(existing, "="); existingKey == key {
				env[index] = entry
				replaced = true
			}
		}
		if !replaced {
			env = append(env, entry)
		}
	}
	command = request.Command
	if len(command) == 0 {
		command = []string{environmentValue(env, "SHELL"), "-l"}
	}
	return command, env
}

func TestStaleHelperGetsAccountShellFromClientAuthoritativeCreate(t *testing.T) {
	zsh := streamFakeShell(t, "zsh")
	staleEnv := []string{"SHELL=/bin/bash", "PATH=/usr/bin:/bin"}
	request := map[string]any{"id": "tab-1", "env": []string{"TERM=xterm-256color", "COLORTERM=truecolor"}, "cols": 80, "rows": 24}

	// Before: the request as the client sends it. The helper falls back to its own stale SHELL and PATH.
	command, env := helperStartedBadly(readCreate(t, createFrame(t, request)), staleEnv)
	if command[0] != "/bin/bash" || environmentValue(env, "PATH") != "/usr/bin:/bin" {
		t.Fatalf("unfiltered session = %v PATH=%q; the stale helper environment should win here", command, environmentValue(env, "PATH"))
	}

	// After: the host's bridge writes the shell and PATH into the request.
	loginPath := "/opt/homebrew/bin:/usr/bin:/bin"
	forward, token := NewStreamFilter(streamDefaults(zsh, loginPath)).Filter(createFrame(t, request))
	if token != "" {
		t.Fatalf("unexpected replacement token %q", token)
	}
	command, env = helperStartedBadly(readCreate(t, forward), staleEnv)
	if !streamSlicesEqual(command, []string{zsh, "-l"}) {
		t.Fatalf("command = %v, want %v", command, []string{zsh, "-l"})
	}
	if environmentValue(env, "SHELL") != zsh || environmentValue(env, "PATH") != loginPath {
		t.Fatalf("SHELL=%q PATH=%q, want %q and %q", environmentValue(env, "SHELL"), environmentValue(env, "PATH"), zsh, loginPath)
	}
	if environmentValue(env, "TERM") != "xterm-256color" || environmentValue(env, "COLORTERM") != "truecolor" {
		t.Fatalf("the client's own variables were lost: %v", env)
	}

	// A current helper must resolve the very same shell from the rewritten request.
	if launched := defaultShellCommand(readCreate(t, forward).Env); launched[0] != zsh {
		t.Fatalf("current helper launches %v, want %s", launched, zsh)
	}
}

func streamSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStreamFilterKeepsWhatTheClientChose(t *testing.T) {
	zsh := streamFakeShell(t, "zsh")
	fish := streamFakeShell(t, "fish")
	request := map[string]any{
		"id": "t", "command": []string{"/usr/bin/top"}, "dir": "/tmp", "future_field": map[string]any{"a": 1},
		"env": []string{"SHELL=" + fish, "PATH=/custom"},
	}
	forward, _ := NewStreamFilter(streamDefaults(zsh, "/login/path")).Filter(createFrame(t, request))
	msg := readCreate(t, forward)
	if !streamSlicesEqual(msg.Command, []string{"/usr/bin/top"}) || environmentValue(msg.Env, "SHELL") != fish || environmentValue(msg.Env, "PATH") != "/custom" || msg.Dir != "/tmp" {
		t.Fatalf("client choices were overridden: %+v", msg)
	}
	_, payload, _ := readFrame(bytes.NewReader(forward))
	if !strings.Contains(string(payload), `"future_field":{"a":1}`) {
		t.Fatalf("unknown field dropped: %s", payload)
	}

	// A client that asked for its own shell without a command gets that shell as the command.
	forward, _ = NewStreamFilter(streamDefaults(zsh, "")).Filter(createFrame(t, map[string]any{"env": []string{"SHELL=" + fish}}))
	if msg := readCreate(t, forward); !streamSlicesEqual(msg.Command, []string{fish, "-l"}) {
		t.Fatalf("command = %v, want the requested shell", msg.Command)
	}

	// A one-shot exec takes no shell command, only the environment.
	forward, _ = NewStreamFilter(streamDefaults(zsh, "/login/path")).Filter(createFrame(t, map[string]any{"command": []string{"ls"}, "pty": false}))
	if msg := readCreate(t, forward); !streamSlicesEqual(msg.Command, []string{"ls"}) || environmentValue(msg.Env, "PATH") != "/login/path" {
		t.Fatalf("exec create = %+v", msg)
	}
	forward, _ = NewStreamFilter(streamDefaults(zsh, "")).Filter(createFrame(t, map[string]any{"pty": false}))
	if msg := readCreate(t, forward); len(msg.Command) != 0 {
		t.Fatalf("exec create got a shell command: %v", msg.Command)
	}
}

func TestStreamFilterIsTransparentForOtherFramesAndSplitInput(t *testing.T) {
	zsh := streamFakeShell(t, "zsh")
	var stream []byte
	stream = appendFrame(stream, frameHello, []byte(`{"version":3}`))
	stream = appendFrame(stream, frameInput, bytes.Repeat([]byte("x"), 1000))
	stream = appendFrame(stream, frameList, []byte("{}"))
	created := createFrame(t, map[string]any{"id": "a"})
	stream = append(stream, created...)
	stream = appendFrame(stream, frameResize, []byte(`{"cols":1}`))
	stream = appendFrame(stream, frameCredit, nil)

	whole, _ := NewStreamFilter(streamDefaults(zsh, "/p")).Filter(stream)
	var split []byte
	filter := NewStreamFilter(streamDefaults(zsh, "/p"))
	for index := 0; index < len(stream); index++ {
		out, _ := filter.Filter(stream[index : index+1])
		split = append(split, out...)
	}
	if !bytes.Equal(whole, split) {
		t.Fatal("byte-by-byte input produced different output than one write")
	}
	// Everything except the create frame is byte-identical, in order.
	rewritten := appendFrame(nil, frameCreate, streamPayload(t, whole, frameCreate))
	want := bytes.Replace(stream, created, rewritten, 1)
	if !bytes.Equal(whole, want) {
		t.Fatal("frames other than create were altered")
	}
}

func streamPayload(t *testing.T, stream []byte, want frameType) []byte {
	t.Helper()
	reader := bytes.NewReader(stream)
	for {
		kind, payload, err := readFrame(reader)
		if err != nil {
			t.Fatal(err)
		}
		if kind == want {
			return payload
		}
	}
}

func TestStreamFilterInterceptsOnlySameProtocolReplacement(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	same := HelperConsentToken(RunningHelper{Protocol: ProtocolVersion, PID: 5, ActivityKnown: true, StartTime: 7, StateDigest: digest})
	older := HelperConsentToken(RunningHelper{Protocol: ProtocolVersion - 1, PID: 5, ActivityKnown: true, StartTime: 7, StateDigest: digest})
	hello := func(token string) []byte {
		payload, _ := json.Marshal(helloMsg{Version: ProtocolVersion, ReplaceLegacy: token})
		return appendFrame(nil, frameHello, payload)
	}
	forward, token := NewStreamFilter(CreateDefaults{}).Filter(hello(same))
	if token != same || len(forward) != 0 {
		t.Fatalf("same-protocol replacement: token %q, forwarded %d bytes", token, len(forward))
	}
	original := hello(older)
	forward, token = NewStreamFilter(CreateDefaults{}).Filter(original)
	if token != "" || !bytes.Equal(forward, original) {
		t.Fatalf("an older-protocol replacement must reach the bridge unchanged, got token %q", token)
	}
}

func TestStaleAgainstNeedsSameProtocolAndDifferentDigest(t *testing.T) {
	local := RuntimeMetadata{Version: "1.3.1", SHA256: strings.Repeat("a", 64)}
	other := &RuntimeMetadata{Version: "1.3.0", SHA256: strings.Repeat("b", 64)}
	for name, tc := range map[string]struct {
		helper RunningHelper
		want   bool
	}{
		"older build":       {RunningHelper{Protocol: ProtocolVersion, Runtime: other}, true},
		"same build":        {RunningHelper{Protocol: ProtocolVersion, Runtime: &local}, false},
		"unreported build":  {RunningHelper{Protocol: ProtocolVersion}, false},
		"older protocol":    {RunningHelper{Protocol: ProtocolVersion - 1, Runtime: other}, false},
		"unreported digest": {RunningHelper{Protocol: ProtocolVersion, Runtime: &RuntimeMetadata{Version: "x"}}, false},
	} {
		if got := tc.helper.StaleAgainst(local); got != tc.want {
			t.Errorf("%s: StaleAgainst = %v, want %v", name, got, tc.want)
		}
	}
}

func TestStaleHelperNoticeText(t *testing.T) {
	build := HelperBuild{
		Helper: RunningHelper{Protocol: ProtocolVersion, ActivityKnown: true, Activity: ActivitySnapshot{PTY: 2},
			Runtime: &RuntimeMetadata{Version: "1.3.0", SHA256: strings.Repeat("b", 64)}},
		Local: RuntimeMetadata{Version: "1.3.2", SHA256: strings.Repeat("a", 64)},
		Stale: true, Sessions: 2, SessionsKnown: true,
	}
	want := "主机端组件版本较旧（1.3.0，当前 1.3.2），仍有 2 个持久终端在运行；全部结束后将自动更新。"
	if got := build.Notice(); got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
	build.Sessions = 0
	if got := build.Notice(); got != "" {
		t.Fatalf("a stale helper without sessions is replaced, not reported: %q", got)
	}
}

func streamStaleConsent(sessions int) RunningHelper {
	return RunningHelper{Protocol: ProtocolVersion, PID: 9, ActivityKnown: true, Activity: ActivitySnapshot{PTY: sessions},
		StartTime: 3, StateDigest: strings.Repeat("cd", 32)}
}

func TestSameProtocolReplacementNeverEndsSessionsBeyondConsent(t *testing.T) {
	run := func(consent, current RunningHelper, older bool) (calls []string, err error) {
		steps := replaceSteps{
			sameProtocol: true,
			stillOlder:   func(RunningHelper) bool { return older },
			verify: func(context.Context, int) (RunningHelper, error) {
				calls = append(calls, "verify")
				return current, nil
			},
			stop: func(context.Context, RunningHelper) (StopResult, error) {
				calls = append(calls, "stop")
				return StopResult{}, nil
			},
			start: func(context.Context) (*Helper, error) {
				calls = append(calls, "start")
				return &Helper{}, nil
			},
			restore: func(context.Context, string, int) (bool, error) { return false, errors.New("unexpected") },
		}
		_, _, _, err = steps.run(context.Background(), consent)
		return calls, err
	}

	calls, err := run(streamStaleConsent(0), streamStaleConsent(0), true)
	if err != nil || !streamSlicesEqual(calls, []string{"verify", "stop", "start"}) {
		t.Fatalf("idle stale helper: calls %v, err %v; want verify, stop, start", calls, err)
	}
	// A terminal was opened after the zero-session consent: nothing is stopped.
	calls, err = run(streamStaleConsent(0), streamStaleConsent(1), true)
	var legacy *LegacyHelperError
	if !errors.As(err, &legacy) || !streamSlicesEqual(calls, []string{"verify"}) {
		t.Fatalf("busy helper: calls %v, err %v; want a refusal before stop", calls, err)
	}
	// The helper is no longer an older build (another client replaced it).
	calls, err = run(streamStaleConsent(0), streamStaleConsent(0), false)
	if !errors.As(err, &legacy) || !streamSlicesEqual(calls, []string{"verify"}) {
		t.Fatalf("current helper: calls %v, err %v; want a refusal before stop", calls, err)
	}
	// Explicit consent for N sessions still covers a helper with fewer.
	calls, err = run(streamStaleConsent(2), streamStaleConsent(1), true)
	if err != nil || !streamSlicesEqual(calls, []string{"verify", "stop", "start"}) {
		t.Fatalf("consented replacement: calls %v, err %v", calls, err)
	}
}

func TestEnsureHelperBuildWithoutHelperIsQuiet(t *testing.T) {
	build, err := ensureHelperBuild(context.Background(), HelperConfig{StateDir: t.TempDir()}, RuntimeMetadata{SHA256: strings.Repeat("a", 64)})
	if err != nil || build.Replaced || build.Stale {
		t.Fatalf("no helper: %+v, %v", build, err)
	}
}
