package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/local"
	"github.com/cloudwego/eino/schema"
)

func TestAllowSessionOnlySuppressesSameKindWithinJob(t *testing.T) {
	var actions atomic.Int64
	chat := sequenceModel(
		toolCallMessage(namedToolCall("start", "docker_control", `{"container_id":"web","action":"start"}`)),
		toolCallMessage(namedToolCall("stop", "docker_control", `{"container_id":"web","action":"stop"}`)),
		schema.AssistantMessage("done", nil),
	)
	runner, _ := testRunner(t, chat, tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { actions.Add(1); return nil }}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow_session"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if actions.Load() != 2 {
		t.Fatalf("actions=%d", actions.Load())
	}
	confirmations := 0
	for _, event := range events {
		if event.Type == "confirmRequired" {
			confirmations++
		}
	}
	if confirmations != 1 {
		t.Fatalf("confirmations=%d events=%+v", confirmations, events)
	}
}

func localTransport(t *testing.T) (base.Transport, string) {
	t.Helper()
	directory := t.TempDir()
	transport := local.NewWithConfig(local.Config{CWD: directory})
	t.Cleanup(func() { _ = transport.Close() })
	return transport, directory
}

func localDeps(transport base.Transport) tools.Dependencies {
	return tools.Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }}
}

func TestEinoWriteRechecksVersionAfterConfirmation(t *testing.T) {
	transport, directory := localTransport(t)
	path := filepath.Join(directory, "config.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	encodedPath, _ := json.Marshal(path)
	chat := sequenceModel(
		toolCallMessage(namedToolCall("read", "read_file", `{"path":`+string(encodedPath)+`}`)),
		toolCallMessage(namedToolCall("write", "write_file", `{"path":`+string(encodedPath)+`,"content":"approved"}`)),
		schema.AssistantMessage("done", nil),
	)
	runner, _ := testRunner(t, chat, localDeps(transport), 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")
	if err := os.WriteFile(path, []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	var result Event
	for _, event := range events {
		if event.Type == "toolResult" && event.ID == "write" {
			result = event
			break
		}
	}
	if result.OK || !strings.Contains(result.Text, tools.ErrFileChanged.Error()) {
		t.Fatalf("result=%+v", result)
	}
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("terminal events done=%d error=%d", done, failed)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "external" {
		t.Fatalf("content=%q err=%v", content, err)
	}
}

func TestEinoFileChangeEventPrecedesToolResult(t *testing.T) {
	transport, directory := localTransport(t)
	path := filepath.Join(directory, "new.txt")
	encodedPath, _ := json.Marshal(path)
	chat := sequenceModel(
		toolCallMessage(namedToolCall("read", "read_file", `{"path":`+string(encodedPath)+`}`)),
		toolCallMessage(namedToolCall("write", "write_file", `{"path":`+string(encodedPath)+`,"content":"new"}`)),
		schema.AssistantMessage("done", nil),
	)
	runner, _ := testRunner(t, chat, localDeps(transport), 0)
	runner.config.Permission = func(context.Context) (guard.Config, error) { return guard.Config{Mode: guard.Silent}, nil }
	stream := &SliceStream{}
	startTestJob(t, runner, stream, "go")
	events := waitClosed(t, stream)
	changeIndex, resultIndex := -1, -1
	for i, event := range events {
		if event.Type == "fileChange" && changeIndex == -1 {
			changeIndex = i
			if event.Before != "" || event.After != "new" {
				t.Fatalf("change=%+v", event)
			}
		}
		if event.Type == "toolResult" && event.ID == "write" && resultIndex == -1 {
			resultIndex = i
		}
	}
	if changeIndex < 0 || resultIndex < 0 || changeIndex > resultIndex {
		t.Fatalf("changeIndex=%d resultIndex=%d events=%+v", changeIndex, resultIndex, events)
	}
}

func TestMultipleHITLRequestsInOneTurnResumeSequentially(t *testing.T) {
	var actions atomic.Int64
	chat := sequenceModel(
		toolCallMessage(
			namedToolCall("start", "docker_control", `{"container_id":"web","action":"start"}`),
			namedToolCall("stop", "docker_control", `{"container_id":"web","action":"stop"}`),
		),
		schema.AssistantMessage("done", nil),
	)
	runner, _ := testRunner(t, chat, tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { actions.Add(1); return nil }}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	first := waitEventIndex(t, stream, "confirmRequired", 0)
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: first.ID, Nonce: first.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	second := waitEventIndex(t, stream, "confirmRequired", 1)
	if second.ID == first.ID {
		t.Fatalf("second confirmation did not advance: first=%+v second=%+v", first, second)
	}
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: second.ID, Nonce: second.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if actions.Load() != 2 {
		t.Fatalf("actions=%d events=%+v", actions.Load(), events)
	}
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
}

func waitEventIndex(t *testing.T, stream *SliceStream, kind string, index int) Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, _ := stream.Snapshot()
		seen := 0
		for _, event := range events {
			if event.Type != kind {
				continue
			}
			if seen == index {
				return event
			}
			seen++
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s[%d]", kind, index)
	return Event{}
}
