package winrm

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type lifecycleStage string

const (
	stageCreate       lifecycleStage = "create"
	stageExecute      lifecycleStage = "execute"
	stageReceive      lifecycleStage = "receive"
	stageSignal       lifecycleStage = "signal"
	stageDelete       lifecycleStage = "delete"
	stageResponseBody lifecycleStage = "response-body"
)

type lifecycleMode string

const (
	modeTimeout lifecycleMode = "timeout"
	modeCancel  lifecycleMode = "cancel"
	modeClose   lifecycleMode = "close"
)

func lifecycleAction(body string) lifecycleStage {
	switch {
	case strings.Contains(body, "transfer/Create"):
		return stageCreate
	case strings.Contains(body, "shell/Command"):
		return stageExecute
	case strings.Contains(body, "shell/Receive"):
		return stageReceive
	case strings.Contains(body, "shell/Signal"):
		return stageSignal
	case strings.Contains(body, "transfer/Delete"):
		return stageDelete
	default:
		return ""
	}
}

func lifecycleResponse(action, blocked lifecycleStage) string {
	switch action {
	case stageCreate:
		return fixtureCreateShellResponse
	case stageExecute:
		return fixtureExecuteCommandResponse
	case stageReceive:
		if blocked == stageSignal {
			return "not XML"
		}
		return `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:rsp="http://schemas.microsoft.com/wbem/wsman/1/windows/shell"><s:Body><rsp:ReceiveResponse><rsp:Stream Name="stdout" CommandId="fixture-command">` + base64.StdEncoding.EncodeToString([]byte("done\n"+cwdSentinelHead+`C:\`+cwdSentinelTail+"\r\n")) + `</rsp:Stream><rsp:CommandState CommandId="fixture-command" State="http://schemas.microsoft.com/wbem/wsman/1/windows/shell/CommandState/Done"><rsp:ExitCode>0</rsp:ExitCode></rsp:CommandState></rsp:ReceiveResponse></s:Body></s:Envelope>`
	default:
		return `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"/>`
	}
}

func TestProductionSOAPLifecycleIsBounded(t *testing.T) {
	stages := []lifecycleStage{stageCreate, stageExecute, stageReceive, stageSignal, stageDelete, stageResponseBody}
	modes := []lifecycleMode{modeTimeout, modeCancel, modeClose}
	for _, stage := range stages {
		for _, mode := range modes {
			t.Run(string(stage)+"/"+string(mode), func(t *testing.T) {
				testProductionSOAPLifecycleBound(t, stage, mode)
			})
		}
	}
}

func testProductionSOAPLifecycleBound(t *testing.T, blocked lifecycleStage, mode lifecycleMode) {
	t.Helper()
	started := make(chan struct{})
	var startedOnce sync.Once
	requestCanceled := make(chan struct{})
	var requestCanceledOnce sync.Once
	var fallback atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		action := lifecycleAction(string(body))
		if blocked == stageResponseBody && action == stageCreate {
			startedOnce.Do(func() { close(started) })
			w.Header().Set("Content-Type", "application/soap+xml;charset=UTF-8")
			_, _ = io.WriteString(w, `<s:Envelope`)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			select {
			case <-request.Context().Done():
				requestCanceledOnce.Do(func() { close(requestCanceled) })
			case <-time.After(2 * time.Second):
				fallback.Store(true)
			}
			return
		}
		if action == blocked {
			startedOnce.Do(func() { close(started) })
			select {
			case <-request.Context().Done():
				requestCanceledOnce.Do(func() { close(requestCanceled) })
			case <-time.After(2 * time.Second):
				fallback.Store(true)
			}
			return
		}
		if action == stageReceive && blocked == stageSignal {
			w.Header().Set("Content-Type", "application/soap+xml;charset=UTF-8")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "fixture receive failure")
			return
		}
		writeFixtureSOAP(w, lifecycleResponse(action, blocked))
	}))
	defer server.Close()
	host, port := fixtureEndpoint(t, server.URL)
	transport, err := New(Config{Host: host, Port: port, User: "alice", Password: "secret", Auth: AuthBasic, RequestTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	ctx := context.Background()
	cancel := func() {}
	if mode == modeCancel {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	type outcome struct {
		err      error
		duration time.Duration
	}
	finished := make(chan outcome, 1)
	begin := time.Now()
	go func() {
		_, err := transport.Exec(ctx, "Write-Output fixture", base.ExecOptions{Timeout: 80 * time.Millisecond})
		finished <- outcome{err: err, duration: time.Since(begin)}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatalf("SOAP %s request did not start", blocked)
	}
	switch mode {
	case modeCancel:
		cancel()
	case modeClose:
		if err := transport.Close(); err != nil {
			t.Fatal(err)
		}
	}
	var result outcome
	select {
	case result = <-finished:
	case <-time.After(1500 * time.Millisecond):
		t.Fatalf("SOAP %s remained blocked after %s", blocked, mode)
	}
	if result.duration > time.Second {
		t.Fatalf("SOAP %s returned after %s in %s mode", blocked, result.duration, mode)
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatalf("SOAP %s HTTP request context was not canceled", blocked)
	}
	if fallback.Load() {
		t.Fatalf("SOAP %s HTTP request was not bound to its context", blocked)
	}
	var expected error
	switch mode {
	case modeTimeout:
		expected = context.DeadlineExceeded
	case modeCancel:
		expected = context.Canceled
	case modeClose:
		expected = base.ErrClosed
	}
	if !errors.Is(result.err, expected) {
		t.Fatalf("SOAP %s %s error = %v, want %v", blocked, mode, result.err, expected)
	}
}
