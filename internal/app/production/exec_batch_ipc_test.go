package production

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type execBatchTestTransport struct {
	alive atomic.Bool
	exec  func(context.Context, string, base.ExecOptions) (base.ExecResult, error)
}

func newExecBatchTestTransport(exec func(context.Context, string, base.ExecOptions) (base.ExecResult, error)) *execBatchTestTransport {
	transport := &execBatchTestTransport{exec: exec}
	transport.alive.Store(true)
	return transport
}

func (t *execBatchTestTransport) Kind() string       { return "ssh" }
func (t *execBatchTestTransport) Generation() uint64 { return 1 }
func (t *execBatchTestTransport) IsAlive() bool      { return t.alive.Load() }
func (t *execBatchTestTransport) Close() error {
	t.alive.Store(false)
	return nil
}
func (t *execBatchTestTransport) Ping(context.Context) (time.Duration, error) { return 0, nil }
func (t *execBatchTestTransport) Exec(ctx context.Context, command string, options base.ExecOptions) (base.ExecResult, error) {
	return t.exec(ctx, command, options)
}

func newExecBatchTestManager(t *testing.T, transport base.Transport) *session.Manager {
	t.Helper()
	manager := session.NewManager(session.Config{Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		return transport, nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func dispatchExecBatchTest(dispatcher *ipc.Dispatcher, ctx context.Context, command string, input any) ipc.Response {
	payload, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: payload}, ipc.Environment{})
}

func decodeExecBatchTest[T any](t *testing.T, response ipc.Response) T {
	t.Helper()
	var value T
	if !response.OK {
		t.Fatalf("dispatch failed: %+v", response)
	}
	if err := json.Unmarshal(response.Data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestExecBatchIPCPreflightEnforcesOwnerKindAndConnection(t *testing.T) {
	var execCalls atomic.Int32
	transport := newExecBatchTestTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
		execCalls.Add(1)
		code := 0
		return base.ExecResult{ExitCode: &code}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")
	sshSession, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-ssh", Name: "ssh", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	localSession, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-local", Name: "local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	service := newExecBatchIPCService(manager, execBatchTestAssets(nil), nil)
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}

	response := dispatchExecBatchTest(dispatcher, ownerB, "exec_batch_execute", execbatch.Request{
		BatchID: "other-owner", SessionIDs: []string{sshSession.ID}, Command: "hostname",
	})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("other-owner preflight = %+v", response)
	}
	response = dispatchExecBatchTest(dispatcher, ownerA, "exec_batch_execute", execbatch.Request{
		BatchID: "local-target", SessionIDs: []string{localSession.ID}, Command: "hostname",
	})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("local target preflight = %+v", response)
	}
	if err := manager.DisconnectFor(ownerA, sshSession.ID); err != nil {
		t.Fatal(err)
	}
	response = dispatchExecBatchTest(dispatcher, ownerA, "exec_batch_execute", execbatch.Request{
		BatchID: "disconnected-target", SessionIDs: []string{sshSession.ID}, Command: "hostname",
	})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeDisconnected {
		t.Fatalf("disconnected preflight = %+v", response)
	}
	if got := execCalls.Load(); got != 0 {
		t.Fatalf("preflight failures executed %d commands", got)
	}
}

func TestExecBatchIPCCancelRunsParallelAndChecksOwner(t *testing.T) {
	started := make(chan context.Context, 1)
	transport := newExecBatchTestTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
		started <- ctx
		<-ctx.Done()
		return base.ExecResult{}, ctx.Err()
	})
	manager := newExecBatchTestManager(t, transport)
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")
	connected, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-a", Name: "a", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	connectedB, err := manager.ConnectFor(ownerB, session.Asset{ID: "asset-b", Name: "b", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := ipc.NewDispatcher()
	if err := newExecBatchIPCService(manager, execBatchTestAssets(nil), nil).register(dispatcher); err != nil {
		t.Fatal(err)
	}
	done := make(chan ipc.Response, 1)
	go func() {
		done <- dispatchExecBatchTest(dispatcher, ownerA, "exec_batch_execute", execbatch.Request{
			BatchID: "batch-shared", SessionIDs: []string{connected.ID}, Command: "wait",
		})
	}()
	var execCtx context.Context
	select {
	case execCtx = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("execution did not start")
	}
	crossOwner := dispatchExecBatchTest(dispatcher, ownerB, "exec_batch_cancel", execBatchCancelRequest{BatchID: "batch-shared"})
	crossResult := decodeExecBatchTest[execbatch.CancelResult](t, crossOwner)
	if !crossResult.Canceled || crossResult.BatchID != "batch-shared" {
		t.Fatalf("cross-owner cancel = %+v, want an owner-scoped pending cancel", crossResult)
	}
	if execCtx.Err() != nil {
		t.Fatal("owner B canceled owner A batch")
	}
	early := dispatchExecBatchTest(dispatcher, ownerB, "exec_batch_execute", execbatch.Request{
		BatchID: "batch-shared", SessionIDs: []string{connectedB.ID}, Command: "wait",
	})
	earlyBatch := decodeExecBatchTest[execbatch.Batch](t, early)
	if len(earlyBatch.Results) != 1 || earlyBatch.Results[0].Status != execbatch.StatusCanceled {
		t.Fatalf("owner B early-canceled batch = %+v", earlyBatch)
	}
	if execCtx.Err() != nil {
		t.Fatal("owner B pending cancel leaked into owner A's batch")
	}
	canceled := dispatchExecBatchTest(dispatcher, ownerA, "exec_batch_cancel", execBatchCancelRequest{BatchID: "batch-shared"})
	result := decodeExecBatchTest[execbatch.CancelResult](t, canceled)
	if !result.Canceled || result.BatchID != "batch-shared" {
		t.Fatalf("cancel result = %+v", result)
	}
	select {
	case response := <-done:
		batch := decodeExecBatchTest[execbatch.Batch](t, response)
		if len(batch.Results) != 1 || batch.Results[0].Status != execbatch.StatusCanceled {
			t.Fatalf("executed batch = %+v", batch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel RPC did not unblock the execute RPC")
	}
}

func TestExecBatchIPCWithExistingSSHFixture(t *testing.T) {
	server := newSSHConnectorServer(t, nil)
	server.execCommands = make(chan string, 1)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credentialID := createConnectorTestCredential(t, production, "batch-password", "secret")
	assetID := createConnectorTestAsset(t, production, "batch-target", "127.0.0.1", server.port(), "password", credentialID, `{"autoAcceptUnknownHost":true}`)
	connected := connectHostKeyTestAsset(t, production, assetID)
	registered := make(map[string]bool)
	for _, command := range production.Dispatcher.Commands() {
		registered[command] = true
	}
	if !registered["exec_batch_execute"] || !registered["exec_batch_cancel"] {
		t.Fatalf("production session module did not register exec batch commands: %+v", registered)
	}

	cancelResponse := dispatchHostKeyTest(t, production, "exec_batch_cancel", `{"batchId":"early-production-cancel"}`)
	decodeExecBatchTest[execbatch.CancelResult](t, cancelResponse)
	earlyResponse := dispatchHostKeyTest(t, production, "exec_batch_execute", `{"batchId":"early-production-cancel","sessionIds":["`+connected.ID+`"],"command":"early-must-not-start","timeoutMs":2000}`)
	earlyBatch := decodeExecBatchTest[execbatch.Batch](t, earlyResponse)
	if len(earlyBatch.Results) != 1 || earlyBatch.Results[0].Status != execbatch.StatusCanceled {
		t.Fatalf("early-canceled production batch = %+v", earlyBatch)
	}

	response := dispatchHostKeyTest(t, production, "exec_batch_execute", `{"batchId":"real-ssh-batch","sessionIds":["`+connected.ID+`"],"command":"printf batch-fixture","timeoutMs":2000}`)
	batch := decodeExecBatchTest[execbatch.Batch](t, response)
	if len(batch.Results) != 1 || batch.Results[0].Status != execbatch.StatusSuccess || batch.Results[0].ExitCode == nil || *batch.Results[0].ExitCode != 0 {
		t.Fatalf("real SSH batch = %+v", batch)
	}
	select {
	case command := <-server.execCommands:
		if command != "printf batch-fixture" {
			t.Fatalf("SSH command = %q", command)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SSH fixture did not receive the command")
	}
	source, kind := "user", "exec_batch"
	records, err := production.Services.Store.AuditQuery(t.Context(), store.AuditQuery{Source: &source, Kind: &kind})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].SessionID == nil || *records[0].SessionID != connected.ID {
		t.Fatalf("audit records = %+v", records)
	}
	if strings.Contains(records[0].PayloadJSON, "stdout") || strings.Contains(records[0].PayloadJSON, "stderr") {
		t.Fatalf("audit contains output fields: %s", records[0].PayloadJSON)
	}
}

func TestExecBatchIPCErrorMapping(t *testing.T) {
	for _, test := range []struct {
		err  error
		code ipc.Code
	}{
		{execbatch.ErrInvalid, ipc.CodeBadParam},
		{execbatch.ErrConfirmationRequired, ipc.CodeNeedsConfirm},
		{execbatch.ErrBatchNotFound, ipc.CodeNotFound},
		{execbatch.ErrBatchConflict, ipc.Code("conflict")},
		{execbatch.ErrPendingCancelLimit, ipc.Code("conflict")},
		{execbatch.ErrForbidden, ipc.CodeForbidden},
		{execbatch.ErrUnsupported, ipc.CodeUnsupported},
		{execbatch.ErrDisconnected, ipc.CodeDisconnected},
		{context.DeadlineExceeded, ipc.CodeTimeout},
	} {
		err := execBatchIPCError(test.err)
		var appErr *ipc.Error
		if !errors.As(err, &appErr) || appErr.Code != test.code {
			t.Fatalf("map %v = %+v, want %s", test.err, err, test.code)
		}
	}
}
