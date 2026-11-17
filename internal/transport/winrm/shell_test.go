package winrm

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	fakeShellIDPattern  = regexp.MustCompile(`Name="ShellId">([^<]+)<`)
	fakeStdinPattern    = regexp.MustCompile(`Name="stdin"[^>]*>([^<]*)<`)
	fakeSignalPattern   = regexp.MustCompile(`Code>([^<]+)<`)
	fakeCommandPattern  = regexp.MustCompile(`FromBase64String\('([^']*)'\)`)
	fakeTokenPattern    = regexp.MustCompile(`<<NXT:([0-9a-f]+)>>`)
	fakeLocationPattern = regexp.MustCompile(`Set-Location -LiteralPath '([^']*)'`)
)

// fakePowerShellHost is a WinRM endpoint whose shells run a tiny PowerShell
// stand-in: it decodes the stdin lines the interactive shell sends, keeps
// per-process state and answers with the completion markers PowerShell would
// print.
type fakePowerShellHost struct {
	t           *testing.T
	mu          sync.Mutex
	wake        chan struct{}
	createFault string
	commandFail bool
	split       bool
	nextID      int
	processes   map[string]*fakeProcess
	creates     int
	commands    []string
	signals     []string
	deleted     []string
}

type fakeProcess struct {
	vars    map[string]string
	cwd     string
	stdin   []byte
	queue   []streamChunk
	done    bool
	code    int
	running string
	token   string
}

func newFakePowerShellHost(t *testing.T) (*fakePowerShellHost, *Transport) {
	t.Helper()
	host := &fakePowerShellHost{t: t, wake: make(chan struct{}, 1), processes: make(map[string]*fakeProcess)}
	server := httptest.NewServer(host)
	t.Cleanup(server.Close)
	address, port := fixtureEndpoint(t, server.URL)
	transport, err := New(Config{Host: address, Port: port, User: "alice", Password: "secret", Auth: AuthBasic, RequestTimeout: 5 * time.Second, InitialCWD: `C:\Users\alice`})
	if err != nil {
		t.Fatal(err)
	}
	transport.shellTimings = shellTimings{receiveWait: time.Second, interruptGrace: 5 * time.Second, stderrGrace: 2 * time.Second, closeTimeout: 2 * time.Second}
	t.Cleanup(func() { _ = transport.Close() })
	return host, transport
}

func (h *fakePowerShellHost) notify() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *fakePowerShellHost) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	raw, _ := io.ReadAll(request.Body)
	body := string(raw)
	shellID := ""
	if match := fakeShellIDPattern.FindStringSubmatch(body); match != nil {
		shellID = match[1]
	}
	switch lifecycleAction(body) {
	case stageCreate:
		h.mu.Lock()
		h.creates++
		fault := h.createFault
		h.nextID++
		id := "shell-" + strconv.Itoa(h.nextID)
		h.mu.Unlock()
		if fault != "" {
			w.Header().Set("Content-Type", "application/soap+xml;charset=UTF-8")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, fault)
			return
		}
		writeFixtureSOAP(w, strings.Replace(fixtureCreateShellResponse, "fixture-shell", id, 1))
	case stageExecute:
		h.mu.Lock()
		h.commands = append(h.commands, body)
		fail := h.commandFail
		if !fail {
			h.processes[shellID] = &fakeProcess{vars: make(map[string]string)}
		}
		h.mu.Unlock()
		if fail {
			w.Header().Set("Content-Type", "application/soap+xml;charset=UTF-8")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "fixture command failure")
			return
		}
		writeFixtureSOAP(w, fixtureExecuteCommandResponse)
	case stageReceive:
		h.receive(w, shellID)
	case stageSignal:
		code := ""
		if match := fakeSignalPattern.FindStringSubmatch(body); match != nil {
			code = match[1]
		}
		h.mu.Lock()
		h.signals = append(h.signals, code)
		if process := h.processes[shellID]; process != nil {
			switch {
			case code == signalTerminate:
				process.done, process.code = true, -1073741510
			case code == signalCtrlC && process.running == "sleep":
				process.running = ""
				h.completeLocked(process, false, 1)
				h.runLinesLocked(process)
			}
		}
		h.mu.Unlock()
		h.notify()
		writeFixtureSOAP(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"/>`)
	case stageDelete:
		h.mu.Lock()
		h.deleted = append(h.deleted, shellID)
		h.mu.Unlock()
		writeFixtureSOAP(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"/>`)
	default:
		if match := fakeStdinPattern.FindStringSubmatch(body); match != nil {
			data, err := base64.StdEncoding.DecodeString(match[1])
			if err != nil {
				h.t.Errorf("stdin is not base64: %v", err)
			}
			h.mu.Lock()
			if process := h.processes[shellID]; process != nil {
				process.stdin = append(process.stdin, data...)
				h.runLinesLocked(process)
			}
			h.mu.Unlock()
			h.notify()
		}
		writeFixtureSOAP(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"/>`)
	}
}

func (h *fakePowerShellHost) receive(w http.ResponseWriter, shellID string) {
	deadline := time.After(30 * time.Millisecond)
	for {
		h.mu.Lock()
		process := h.processes[shellID]
		if process != nil && (len(process.queue) > 0 || process.done) {
			var chunks []streamChunk
			if h.split && len(process.queue) > 0 {
				chunks, process.queue = process.queue[:1], process.queue[1:]
			} else {
				chunks, process.queue = process.queue, nil
			}
			done := process.done && len(process.queue) == 0
			code := process.code
			h.mu.Unlock()
			var response strings.Builder
			response.WriteString(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:rsp="http://schemas.microsoft.com/wbem/wsman/1/windows/shell"><s:Body><rsp:ReceiveResponse>`)
			for _, chunk := range chunks {
				name := "stdout"
				if chunk.stderr {
					name = "stderr"
				}
				fmt.Fprintf(&response, `<rsp:Stream Name="%s" CommandId="fixture-command">%s</rsp:Stream>`, name, base64.StdEncoding.EncodeToString(chunk.data))
			}
			if done {
				fmt.Fprintf(&response, `<rsp:CommandState CommandId="fixture-command" State="%s"><rsp:ExitCode>%d</rsp:ExitCode></rsp:CommandState>`, commandStateDone, code)
			}
			response.WriteString(`</rsp:ReceiveResponse></s:Body></s:Envelope>`)
			writeFixtureSOAP(w, response.String())
			return
		}
		h.mu.Unlock()
		select {
		case <-h.wake:
		case <-deadline:
			w.Header().Set("Content-Type", "application/soap+xml;charset=UTF-8")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><s:Fault><s:Code><s:Subcode><s:Value>w:TimedOut</s:Value></s:Subcode></s:Code><s:Detail><f:WSManFault xmlns:f="http://schemas.microsoft.com/wbem/wsman/1/wsmanfault" Code="2150858793"/></s:Detail></s:Fault></s:Body></s:Envelope>`)
			return
		}
	}
}

func (h *fakePowerShellHost) runLinesLocked(process *fakeProcess) {
	for !process.done && process.running == "" {
		index := bytes.IndexByte(process.stdin, '\n')
		if index < 0 {
			return
		}
		line := string(process.stdin[:index])
		process.stdin = process.stdin[index+1:]
		encoded := fakeCommandPattern.FindStringSubmatch(line)
		token := fakeTokenPattern.FindStringSubmatch(line)
		if encoded == nil || token == nil {
			h.t.Errorf("stdin line is not a wrapped command: %q", line)
			continue
		}
		script, err := base64.StdEncoding.DecodeString(encoded[1])
		if err != nil {
			h.t.Errorf("command is not base64: %v", err)
			continue
		}
		process.token = token[1]
		h.executeLocked(process, string(script))
	}
}

func (h *fakePowerShellHost) emitLocked(process *fakeProcess, stderr bool, text string) {
	process.queue = append(process.queue, streamChunk{stderr: stderr, data: []byte(text)})
}

func (h *fakePowerShellHost) executeLocked(process *fakeProcess, script string) {
	if strings.Contains(script, "OutputEncoding") {
		if match := fakeLocationPattern.FindStringSubmatch(script); match != nil {
			process.cwd = match[1]
		}
		h.completeLocked(process, true, 0)
		return
	}
	fields := strings.Fields(script)
	switch {
	case len(fields) == 3 && fields[1] == "=" && strings.HasPrefix(fields[0], "$"):
		process.vars[fields[0]] = fields[2]
		h.completeLocked(process, true, 0)
	case len(fields) == 2 && fields[0] == "echo":
		value := fields[1]
		if strings.HasPrefix(value, "$") {
			value = process.vars[value]
		}
		h.emitLocked(process, false, value+"\r\n")
		h.completeLocked(process, true, 0)
	case len(fields) == 2 && fields[0] == "cd":
		process.cwd = fields[1]
		h.completeLocked(process, true, 0)
	case script == "pwd":
		h.emitLocked(process, false, process.cwd+"\r\n")
		h.completeLocked(process, true, 0)
	case script == "fail":
		h.emitLocked(process, true, "boom\r\n")
		h.completeLocked(process, false, 1)
	case len(fields) == 2 && fields[0] == "native":
		code, _ := strconv.Atoi(fields[1])
		h.completeLocked(process, code == 0, code)
	case script == "nonewline":
		h.emitLocked(process, false, "partial")
		h.completeLocked(process, true, 0)
	case script == "interleave":
		h.emitLocked(process, false, "out1\r\n")
		h.emitLocked(process, true, "err1\r\n")
		h.emitLocked(process, false, "out2\r\n")
		h.completeLocked(process, true, 0)
	case script == "split":
		h.emitLocked(process, false, "héllo 世界\r\n")
		h.completeLocked(process, true, 0)
		var joined []byte
		var stderr []byte
		for _, chunk := range process.queue {
			if chunk.stderr {
				stderr = append(stderr, chunk.data...)
			} else {
				joined = append(joined, chunk.data...)
			}
		}
		process.queue = []streamChunk{{stderr: true, data: stderr}}
		for len(joined) > 0 {
			size := min(3, len(joined))
			process.queue = append(process.queue, streamChunk{data: joined[:size]})
			joined = joined[size:]
		}
	case script == "sleep", script == "hang":
		process.running = script
	case len(fields) == 2 && fields[0] == "exit":
		process.code, _ = strconv.Atoi(fields[1])
		process.done = true
	default:
		h.emitLocked(process, true, "unknown command\r\n")
		h.completeLocked(process, false, 1)
	}
}

func (h *fakePowerShellHost) completeLocked(process *fakeProcess, ok bool, code int) {
	flag := "0"
	if ok {
		flag = "1"
	}
	h.emitLocked(process, true, string(errorMarker(process.token)))
	h.emitLocked(process, false, string(outputStartMarker(process.token))+flag+":"+strconv.Itoa(code)+":"+base64.StdEncoding.EncodeToString([]byte(process.cwd))+string(outputEndMarker(process.token)))
}

func (h *fakePowerShellHost) snapshot() (creates int, commands int, signals []string, deleted []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.creates, len(h.commands), append([]string(nil), h.signals...), append([]string(nil), h.deleted...)
}

func (h *fakePowerShellHost) running(shellID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if process := h.processes[shellID]; process != nil {
		return process.running
	}
	return ""
}

type outputRecorder struct {
	mu     sync.Mutex
	chunks []recordedOutput
}

type recordedOutput struct {
	stderr bool
	text   string
}

func (r *outputRecorder) write(stderr bool, text string) {
	r.mu.Lock()
	r.chunks = append(r.chunks, recordedOutput{stderr: stderr, text: text})
	r.mu.Unlock()
}

func (r *outputRecorder) take() (stdout, stderr string, chunks []recordedOutput) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out, errText strings.Builder
	for _, chunk := range r.chunks {
		if chunk.stderr {
			errText.WriteString(chunk.text)
		} else {
			out.WriteString(chunk.text)
		}
	}
	chunks = r.chunks
	r.chunks = nil
	return out.String(), errText.String(), chunks
}

func openTestShell(t *testing.T, transport *Transport) (*psShell, *outputRecorder) {
	t.Helper()
	recorder := &outputRecorder{}
	shell, err := transport.OpenShell(context.Background(), ShellOptions{Output: recorder.write})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shell.Close() })
	return shell.(*psShell), recorder
}

func runShell(t *testing.T, shell InteractiveShell, command string) CommandResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := shell.Run(ctx, command)
	if err != nil {
		t.Fatalf("Run(%q): %v", command, err)
	}
	return result
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestInteractiveShellKeepsStateAcrossCommands(t *testing.T) {
	host, transport := newFakePowerShellHost(t)
	shell, recorder := openTestShell(t, transport)
	runShell(t, shell, "$x = 42")
	if result := runShell(t, shell, "echo $x"); !result.Success || result.ExitCode != 0 {
		t.Fatalf("echo result = %+v", result)
	}
	if stdout, stderr, _ := recorder.take(); stdout != "42\r\n" || stderr != "" {
		t.Fatalf("output = %q / %q", stdout, stderr)
	}
	creates, commands, _, _ := host.snapshot()
	if creates != 1 || commands != 1 {
		t.Fatalf("one PowerShell must serve every command: creates=%d commands=%d", creates, commands)
	}
	host.mu.Lock()
	command := host.commands[0]
	host.mu.Unlock()
	if !strings.Contains(command, interactivePowerShell) {
		t.Fatalf("command line = %q", command)
	}
}

func TestInteractiveShellStreamsOutputInArrivalOrder(t *testing.T) {
	_, transport := newFakePowerShellHost(t)
	shell, recorder := openTestShell(t, transport)
	runShell(t, shell, "interleave")
	_, _, chunks := recorder.take()
	want := []recordedOutput{{text: "out1\r\n"}, {stderr: true, text: "err1\r\n"}, {text: "out2\r\n"}}
	if fmt.Sprint(chunks) != fmt.Sprint(want) {
		t.Fatalf("output order = %+v, want %+v", chunks, want)
	}
}

func TestInteractiveShellStripsMarkersSplitAcrossReceives(t *testing.T) {
	host, transport := newFakePowerShellHost(t)
	shell, recorder := openTestShell(t, transport)
	host.mu.Lock()
	host.split = true
	host.mu.Unlock()
	result := runShell(t, shell, "split")
	stdout, stderr, _ := recorder.take()
	if stdout != "héllo 世界\r\n" || stderr != "" {
		t.Fatalf("output = %q / %q", stdout, stderr)
	}
	if !result.Success || result.CWD != `C:\Users\alice` {
		t.Fatalf("result = %+v", result)
	}
}

func TestInteractiveShellOutputWithoutTrailingNewline(t *testing.T) {
	_, transport := newFakePowerShellHost(t)
	shell, recorder := openTestShell(t, transport)
	runShell(t, shell, "nonewline")
	if stdout, _, _ := recorder.take(); stdout != "partial" {
		t.Fatalf("output = %q", stdout)
	}
	runShell(t, shell, "echo next")
	if stdout, _, _ := recorder.take(); stdout != "next\r\n" {
		t.Fatalf("output after a partial line = %q", stdout)
	}
}

func TestInteractiveShellTracksLocationAndExitCodes(t *testing.T) {
	_, transport := newFakePowerShellHost(t)
	shell, recorder := openTestShell(t, transport)
	runShell(t, shell, "pwd")
	if stdout, _, _ := recorder.take(); stdout != "C:\\Users\\alice\r\n" {
		t.Fatalf("initial location = %q", stdout)
	}
	result := runShell(t, shell, `cd D:\work`)
	if result.CWD != `D:\work` || transport.CWD() != `D:\work` {
		t.Fatalf("location after cd = %q, transport = %q", result.CWD, transport.CWD())
	}
	result = runShell(t, shell, "fail")
	if result.Success || result.ExitCode != 1 {
		t.Fatalf("failed command = %+v", result)
	}
	if _, stderr, _ := recorder.take(); stderr != "boom\r\n" {
		t.Fatalf("stderr = %q", stderr)
	}
	if result := runShell(t, shell, "native 3"); result.Success || result.ExitCode != 3 {
		t.Fatalf("native exit = %+v", result)
	}
}

func TestInteractiveShellInterruptKeepsSession(t *testing.T) {
	host, transport := newFakePowerShellHost(t)
	shell, recorder := openTestShell(t, transport)
	runShell(t, shell, "$x = kept")
	type outcome struct {
		result CommandResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, err := shell.Run(context.Background(), "sleep")
		finished <- outcome{result, err}
	}()
	waitUntil(t, func() bool { return host.running(shell.shellID) == "sleep" })
	if err := shell.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	var got outcome
	select {
	case got = <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("interrupted command did not finish")
	}
	if got.err != nil || got.result.Success || got.result.ExitCode != 1 {
		t.Fatalf("interrupted command = %+v, %v", got.result, got.err)
	}
	_, _, signals, _ := host.snapshot()
	if len(signals) != 1 || signals[0] != signalCtrlC {
		t.Fatalf("signals = %v", signals)
	}
	recorder.take()
	runShell(t, shell, "echo $x")
	if stdout, _, _ := recorder.take(); stdout != "kept\r\n" {
		t.Fatalf("state after Ctrl+C = %q", stdout)
	}
}

func TestInteractiveShellTerminatesCommandIgnoringInterrupt(t *testing.T) {
	host, transport := newFakePowerShellHost(t)
	transport.shellTimings.interruptGrace = 20 * time.Millisecond
	shell, _ := openTestShell(t, transport)
	finished := make(chan error, 1)
	go func() {
		_, err := shell.Run(context.Background(), "hang")
		finished <- err
	}()
	waitUntil(t, func() bool { return host.running(shell.shellID) == "hang" })
	if err := shell.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, ErrInterruptTimeout) {
			t.Fatalf("Run error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command ignoring Ctrl+C was not ended")
	}
	<-shell.Done()
	if err := shell.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, signals, deleted := host.snapshot()
	if len(signals) != 2 || signals[0] != signalCtrlC || signals[1] != signalTerminate {
		t.Fatalf("signals = %v", signals)
	}
	if len(deleted) != 1 || deleted[0] != shell.shellID {
		t.Fatalf("deleted shells = %v", deleted)
	}
}

func TestInteractiveShellReportsProcessExit(t *testing.T) {
	_, transport := newFakePowerShellHost(t)
	shell, _ := openTestShell(t, transport)
	_, err := shell.Run(context.Background(), "exit 4")
	var exit *ShellExitError
	if !errors.As(err, &exit) || exit.Code != 4 {
		t.Fatalf("Run error = %v", err)
	}
	if _, err := shell.Run(context.Background(), "echo again"); !errors.As(err, &exit) {
		t.Fatalf("Run on an exited shell = %v", err)
	}
}

func TestOpenShellReportsShellQuota(t *testing.T) {
	host, transport := newFakePowerShellHost(t)
	host.createFault = `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><s:Fault><s:Detail><f:WSManFault xmlns:f="http://schemas.microsoft.com/wbem/wsman/1/wsmanfault" Code="2150859174"><f:Message>The WS-Management service cannot process the request. This user is allowed a maximum number of 5 concurrent shells, which has been exceeded.</f:Message></f:WSManFault></s:Detail></s:Fault></s:Body></s:Envelope>`
	_, err := transport.OpenShell(context.Background(), ShellOptions{})
	if !errors.Is(err, ErrShellLimit) {
		t.Fatalf("OpenShell error = %v", err)
	}
	if _, _, _, deleted := host.snapshot(); len(deleted) != 0 {
		t.Fatalf("no shell was created, but deleted %v", deleted)
	}
}

func TestOpenShellDeletesShellWhenPowerShellFailsToStart(t *testing.T) {
	host, transport := newFakePowerShellHost(t)
	host.commandFail = true
	if _, err := transport.OpenShell(context.Background(), ShellOptions{}); err == nil || errors.Is(err, ErrShellLimit) {
		t.Fatalf("OpenShell error = %v", err)
	}
	if _, _, _, deleted := host.snapshot(); len(deleted) != 1 || deleted[0] != "shell-1" {
		t.Fatalf("deleted shells = %v", deleted)
	}
}

func TestShellCloseAndTransportCloseDeleteRemoteShells(t *testing.T) {
	host, transport := newFakePowerShellHost(t)
	first, _ := openTestShell(t, transport)
	second, _ := openTestShell(t, transport)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, signals, deleted := host.snapshot(); len(deleted) != 1 || deleted[0] != first.shellID || len(signals) != 1 || signals[0] != signalTerminate {
		t.Fatalf("after shell close: deleted=%v signals=%v", deleted, signals)
	}
	if _, err := first.Run(context.Background(), "echo x"); !errors.Is(err, ErrShellClosed) {
		t.Fatalf("Run after Close = %v", err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		_, _, _, deleted := host.snapshot()
		return len(deleted) == 2 && deleted[1] == second.shellID
	})
	if _, err := transport.OpenShell(context.Background(), ShellOptions{}); err == nil {
		t.Fatal("OpenShell on a closed transport succeeded")
	}
}

func TestOpenShellUnavailableWithoutEndpoint(t *testing.T) {
	transport := testTransport(t, &fakeRunner{})
	if _, err := transport.OpenShell(context.Background(), ShellOptions{}); !errors.Is(err, ErrShellUnsupported) {
		t.Fatalf("OpenShell error = %v", err)
	}
}

func TestMarkerScannerByteByByte(t *testing.T) {
	stream := []byte("ab<<Nx<<NXT:tok>>1:7:QzpcdA==<</NXT:tok>>tail")
	var scanner markerScanner
	scanner.arm(outputStartMarker("tok"), outputEndMarker("tok"))
	var before, payload, after []byte
	found := false
	for index := range stream {
		chunkBefore, chunkFound, chunkPayload, chunkAfter := scanner.scan(stream[index : index+1])
		if found {
			after = append(after, chunkBefore...)
		} else {
			before = append(before, chunkBefore...)
		}
		if chunkFound {
			found = true
			payload = chunkPayload
			after = append(after, chunkAfter...)
		}
	}
	if !found || string(before) != "ab<<Nx" || string(after) != "tail" {
		t.Fatalf("scan = found %v before %q after %q", found, before, after)
	}
	result, err := parseCompletion(payload)
	if err != nil || !result.Success || result.ExitCode != 7 || result.CWD != `C:\t` {
		t.Fatalf("completion = %+v, %v", result, err)
	}
}

func TestTextDecoderJoinsSplitCharacters(t *testing.T) {
	raw := []byte("é世")
	var decoder textDecoder
	var text strings.Builder
	for index := range raw {
		text.WriteString(decoder.decode(raw[index:index+1], false))
	}
	if text.String() != "é世" {
		t.Fatalf("decoded = %q", text.String())
	}
}

func TestInteractiveCommandLineIsOneASCIILine(t *testing.T) {
	line := interactiveCommandLine("abc", "Write-Output '中文'\n$x = 1")
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\r\n") {
		t.Fatalf("command line spans lines: %q", line)
	}
	for _, r := range line {
		if r > 0x7f {
			t.Fatalf("command line is not ASCII: %q", line)
		}
	}
	match := fakeCommandPattern.FindStringSubmatch(line)
	if match == nil {
		t.Fatal("command is not embedded")
	}
	decoded, _ := base64.StdEncoding.DecodeString(match[1])
	if string(decoded) != "Write-Output '中文'\n$x = 1" {
		t.Fatalf("embedded command = %q", decoded)
	}
}
