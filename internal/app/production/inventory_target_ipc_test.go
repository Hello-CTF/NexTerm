package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/docker"
	fleetinventory "github.com/Hello-CTF/NexTerm/internal/fleet/inventory"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type recordingInventoryRunner struct {
	mu      sync.Mutex
	calls   [][]string
	outputs map[string][]byte
	errors  map[string]error
	block   bool
	started chan struct{}
}

func (r *recordingInventoryRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string{name}, args...))
	r.mu.Unlock()
	if r.started != nil {
		select {
		case r.started <- struct{}{}:
		default:
		}
	}
	if r.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	key := name + " " + strings.Join(args, " ")
	if err := r.errors[key]; err != nil {
		return nil, err
	}
	return r.outputs[key], nil
}

func (r *recordingInventoryRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recordingInventoryRunner) callSnapshot() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.calls...)
}

func kubernetesDiscoveryOutputs() map[string][]byte {
	return map[string][]byte{
		"kubectl config get-contexts -o json": []byte(`{"current-context":"staging","contexts":[{"name":"staging","context":{"cluster":"staging-cluster","user":"reader"}}]}`),
		"kubectl get pods -A -o json":         []byte(`{"items":[{"metadata":{"namespace":"ops","name":"api-0"},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running"}}]}`),
	}
}

func dockerDiscoveryOutput() []byte {
	return []byte("{\"Name\":\"prod\",\"DockerEndpoint\":\"ssh://ops@prod\",\"Current\":true}\n")
}

func requireInventoryIPCError(t *testing.T, response ipc.Response, code ipc.Code) {
	t.Helper()
	if response.OK {
		t.Fatalf("dispatch succeeded, want %s: %+v", code, response)
	}
	if response.Error == nil || response.Error.Code != code {
		t.Fatalf("error = %+v, want code %s", response.Error, code)
	}
}

func newInventoryExecDispatcher(t *testing.T, service *inventoryService) *ipc.Dispatcher {
	t.Helper()
	dispatcher := ipc.NewDispatcher()
	if err := service.registerCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

type fakeInventoryDockerExecutor struct {
	calls   []docker.ExecRequest
	result  docker.ExecResult
	err     error
	block   bool
	started chan struct{}
}

func (f *fakeInventoryDockerExecutor) Exec(ctx context.Context, request docker.ExecRequest) (docker.ExecResult, error) {
	f.calls = append(f.calls, request)
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.block {
		<-ctx.Done()
		return docker.ExecResult{}, ctx.Err()
	}
	return f.result, f.err
}

func newInventorySessionManager(t *testing.T) *session.Manager {
	t.Helper()
	manager := session.NewManager(session.Config{Connector: fakeSSHConnector{}})
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func connectInventoryTestSession(t *testing.T, manager *session.Manager, ownerID string, asset store.AssetRow) *session.Session {
	t.Helper()
	ctx := ipc.WithUserID(context.Background(), ownerID)
	connected, err := manager.ConnectFor(ctx, session.Asset{
		ID: asset.ID, Kind: session.KindSSH, ConfigFingerprint: productionAssetConfigFingerprint(asset),
	})
	if err != nil {
		t.Fatal(err)
	}
	return connected
}

func inventoryInt32Pointer(value int32) *int32 { return &value }

func inventoryDockerExecRequest() inventoryTargetExecRequest {
	return inventoryTargetExecRequest{
		Kind: "docker-context",
		Target: map[string]any{
			"type": "context", "name": "prod", "dockerEndpoint": "ssh://ops@prod.example",
		},
		Action:      "exec",
		Container:   "web",
		Command:     []string{"uname", "-a"},
		OperationID: "op-exec-1",
	}
}

func inventoryKubernetesExecRequest() inventoryTargetExecRequest {
	return inventoryTargetExecRequest{
		Kind: "kubernetes",
		Target: map[string]any{
			"type": "pod", "context": "staging", "namespace": "ops", "pod": "api-0", "container": "app",
		},
		Action:      "exec",
		Command:     []string{"uname", "-a"},
		OperationID: "op-exec-1",
	}
}

func inventoryCancelRequest(operationID string) inventoryTargetExecRequest {
	return inventoryTargetExecRequest{Action: "cancel", OperationID: operationID}
}

func inventoryCancelFor(request inventoryTargetExecRequest) inventoryTargetExecRequest {
	request.Action = "cancel"
	return request
}

func importDockerContext(t *testing.T, dispatcher *ipc.Dispatcher, ctx context.Context, runner *recordingInventoryRunner) {
	t.Helper()
	if runner.outputs == nil {
		runner.outputs = map[string][]byte{}
	}
	runner.outputs["docker context ls --format {{json .}}"] = dockerDiscoveryOutput()
	keys := []string{"context/prod"}
	var synced inventorySyncDTO
	dispatchInventoryTest(t, dispatcher, ctx, "inventory_sync", inventorySyncRequest{Source: store.InventorySourceDockerContext, Keys: &keys}, &synced)
	if synced.Created != 1 {
		t.Fatalf("docker context import = %+v", synced)
	}
}

func TestInventoryServerKubernetesDiscoveryRequiresAuthorization(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	service, _ := newInventoryTestService(t, false)
	runner := &recordingInventoryRunner{outputs: kubernetesDiscoveryOutputs()}
	service.runner = runner
	denied := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	response := dispatchInventoryTest(t, denied, ctx, "inventory_discover", inventoryDiscoverRequest{Source: store.InventorySourceKubernetes}, nil)
	requireInventoryIPCError(t, response, ipc.CodeForbidden)
	if runner.callCount() != 0 {
		t.Fatalf("denied discovery ran commands: %+v", runner.calls)
	}

	t.Setenv(inventoryKubeconfigEnv, "1")
	allowed, _ := newInventoryTestService(t, false)
	allowed.runner = runner
	granted := newInventoryExecDispatcher(t, allowed)
	var discovery inventoryDiscoveryDTO
	dispatchInventoryTest(t, granted, ctx, "inventory_discover", inventoryDiscoverRequest{Source: store.InventorySourceKubernetes}, &discovery)
	if len(discovery.Items) != 2 {
		t.Fatalf("authorized discovery = %+v", discovery)
	}
}

func TestInventoryDesktopKubernetesDiscoveryUnchanged(t *testing.T) {
	service, _ := newInventoryTestService(t, true)
	runner := &recordingInventoryRunner{outputs: kubernetesDiscoveryOutputs()}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	var discovery inventoryDiscoveryDTO
	dispatchInventoryTest(t, dispatcher, ctx, "inventory_discover", inventoryDiscoverRequest{Source: store.InventorySourceKubernetes}, &discovery)
	if len(discovery.Items) != 2 {
		t.Fatalf("desktop discovery = %+v", discovery)
	}
}

func TestInventoryTargetExecDockerDesktopRunsAllowlistedExec(t *testing.T) {
	service, database := newInventoryTestService(t, true)
	runner := &recordingInventoryRunner{outputs: map[string][]byte{
		"docker --context prod exec web uname -a": []byte("Linux\n"),
	}}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	var result inventoryTargetExecResponse
	dispatchInventoryTest(t, dispatcher, context.Background(), "inventory_target_exec", inventoryDockerExecRequest(), &result)
	if result.Output != "Linux\n" {
		t.Fatalf("output = %q", result.Output)
	}
	want := []string{"docker", "--context", "prod", "exec", "web", "uname", "-a"}
	if runner.callCount() != 1 || strings.Join(runner.callSnapshot()[0], " ") != strings.Join(want, " ") {
		t.Fatalf("calls = %+v, want %+v", runner.calls, want)
	}
	rows, err := database.AuditQuery(context.Background(), store.AuditQuery{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Kind != "inventory_target_exec" {
		t.Fatalf("exec audit rows = %+v, err=%v", rows, err)
	}
}

func TestInventoryTargetExecDockerServerRequiresManagedSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	manager := newInventorySessionManager(t)
	executor := &fakeInventoryDockerExecutor{result: docker.ExecResult{Output: "Linux\n"}}
	service := newInventoryService(database, nil, false, t.TempDir(),
		withInventoryWatchInterval(20*time.Millisecond), withInventoryDockerAccess(manager, executor))
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	runner := &recordingInventoryRunner{outputs: map[string][]byte{}}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")

	assetA, err := database.AssetCreate(ctxA, store.AssetInput{Kind: "ssh", Name: "prod-a", Host: stringPointerForInventory("prod.example"), Username: optionalString("ops")})
	if err != nil {
		t.Fatal(err)
	}
	assetB, err := database.AssetCreate(ctxB, store.AssetInput{Kind: "ssh", Name: "prod-b", Host: stringPointerForInventory("prod.example"), Username: optionalString("ops")})
	if err != nil {
		t.Fatal(err)
	}
	otherAsset, err := database.AssetCreate(ctxA, store.AssetInput{Kind: "ssh", Name: "other", Host: stringPointerForInventory("other.example"), Username: optionalString("ops")})
	if err != nil {
		t.Fatal(err)
	}
	sessionA := connectInventoryTestSession(t, manager, "owner-a", assetA)
	sessionB := connectInventoryTestSession(t, manager, "owner-b", assetB)
	sessionOther := connectInventoryTestSession(t, manager, "owner-a", otherAsset)

	// Import stays available for target resolution but grants no exec rights.
	importDockerContext(t, dispatcher, ctxA, runner)
	response := dispatchInventoryTest(t, dispatcher, ctxA, "inventory_target_exec", inventoryDockerExecRequest(), nil)
	requireInventoryIPCError(t, response, ipc.CodeBadParam)
	if len(executor.calls) != 0 {
		t.Fatalf("exec without a managed session reached the docker backend: %+v", executor.calls)
	}

	crossOwner := inventoryDockerExecRequest()
	crossOwner.SessionID = sessionB.ID
	response = dispatchInventoryTest(t, dispatcher, ctxA, "inventory_target_exec", crossOwner, nil)
	requireInventoryIPCError(t, response, ipc.CodeForbidden)
	if len(executor.calls) != 0 {
		t.Fatalf("cross-owner exec reached the docker backend: %+v", executor.calls)
	}

	mismapped := inventoryDockerExecRequest()
	mismapped.SessionID = sessionOther.ID
	response = dispatchInventoryTest(t, dispatcher, ctxA, "inventory_target_exec", mismapped, nil)
	requireInventoryIPCError(t, response, ipc.CodeForbidden)
	if len(executor.calls) != 0 {
		t.Fatalf("endpoint-mismatched exec reached the docker backend: %+v", executor.calls)
	}

	noBackend := inventoryDockerExecRequest()
	noBackend.SessionID = sessionA.ID
	executor.err = docker.ErrNoBackend
	response = dispatchInventoryTest(t, dispatcher, ctxA, "inventory_target_exec", noBackend, nil)
	requireInventoryIPCError(t, response, ipc.CodeUnsupported)
	executor.err = nil
	executor.calls = nil

	allowed := inventoryDockerExecRequest()
	allowed.SessionID = sessionA.ID
	var result inventoryTargetExecResponse
	dispatchInventoryTest(t, dispatcher, ctxA, "inventory_target_exec", allowed, &result)
	if result.Output != "Linux\n" {
		t.Fatalf("exec output = %q", result.Output)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("docker backend calls = %+v", executor.calls)
	}
	call := executor.calls[0]
	if call.SessionID != sessionA.ID || call.Container != "web" || call.Command != "'uname' '-a'" {
		t.Fatalf("docker exec request = %+v", call)
	}
	rows, err := database.AuditQuery(ctxA, store.AuditQuery{Limit: 10})
	if err != nil || len(rows) == 0 || rows[0].Kind != "inventory_target_exec" {
		t.Fatalf("exec audit rows = %+v, err=%v", rows, err)
	}

	// The same host reached through the other owner's own session is legitimate.
	executor.calls = nil
	ownSession := inventoryDockerExecRequest()
	ownSession.SessionID = sessionB.ID
	dispatchInventoryTest(t, dispatcher, ctxB, "inventory_target_exec", ownSession, &result)
	if len(executor.calls) != 1 || executor.calls[0].SessionID != sessionB.ID {
		t.Fatalf("owner-b own-session exec = %+v", executor.calls)
	}
}

func TestInventoryTargetExecDockerServerEndpointSchemes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	manager := newInventorySessionManager(t)
	executor := &fakeInventoryDockerExecutor{result: docker.ExecResult{Output: "Linux\n"}}
	service := newInventoryService(database, nil, false, t.TempDir(),
		withInventoryWatchInterval(20*time.Millisecond), withInventoryDockerAccess(manager, executor))
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	runner := &recordingInventoryRunner{outputs: map[string][]byte{}}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")

	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "prod-host", Host: stringPointerForInventory("prod.example"), Port: inventoryInt32Pointer(22), Username: optionalString("ops")})
	if err != nil {
		t.Fatal(err)
	}
	jumpAsset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "jump-host", Host: stringPointerForInventory("prod.example"), Port: inventoryInt32Pointer(2222), Username: optionalString("ops")})
	if err != nil {
		t.Fatal(err)
	}
	connected := connectInventoryTestSession(t, manager, "owner-a", asset)
	jumpConnected := connectInventoryTestSession(t, manager, "owner-a", jumpAsset)

	// A TCP Docker daemon port is not the session transport port: the same
	// host through an own session is allowed even though 2375 != 22.
	tcpSameHost := inventoryDockerExecRequest()
	tcpSameHost.Target = map[string]any{"type": "context", "name": "prod", "dockerEndpoint": "tcp://prod.example:2375"}
	tcpSameHost.SessionID = connected.ID
	var result inventoryTargetExecResponse
	dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", tcpSameHost, &result)
	if result.Output != "Linux\n" || len(executor.calls) != 1 {
		t.Fatalf("tcp same-host exec = %+v calls=%+v", result, executor.calls)
	}

	// A different host stays fail closed for TCP endpoints.
	executor.calls = nil
	tcpOtherHost := inventoryDockerExecRequest()
	tcpOtherHost.Target = map[string]any{"type": "context", "name": "prod", "dockerEndpoint": "tcp://other.example:2375"}
	tcpOtherHost.SessionID = connected.ID
	response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", tcpOtherHost, nil)
	requireInventoryIPCError(t, response, ipc.CodeForbidden)
	if len(executor.calls) != 0 {
		t.Fatalf("tcp other-host exec reached the backend: %+v", executor.calls)
	}

	// SSH endpoints compare the SSH port: 2222 against asset port 22 fails.
	sshWrongPort := inventoryDockerExecRequest()
	sshWrongPort.Target = map[string]any{"type": "context", "name": "prod", "dockerEndpoint": "ssh://ops@prod.example:2222"}
	sshWrongPort.SessionID = connected.ID
	response = dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", sshWrongPort, nil)
	requireInventoryIPCError(t, response, ipc.CodeForbidden)
	if len(executor.calls) != 0 {
		t.Fatalf("ssh wrong-port exec reached the backend: %+v", executor.calls)
	}

	// A no-port endpoint means SSH port 22 and must not match a 2222 asset.
	noPort := inventoryDockerExecRequest()
	noPort.SessionID = jumpConnected.ID
	response = dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", noPort, nil)
	requireInventoryIPCError(t, response, ipc.CodeForbidden)
	if len(executor.calls) != 0 {
		t.Fatalf("no-port endpoint matched a 2222 asset: %+v", executor.calls)
	}

	// The same no-port endpoint matches port-22 and default-port assets.
	noPort.SessionID = connected.ID
	dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", noPort, &result)
	if len(executor.calls) != 1 {
		t.Fatalf("no-port endpoint on port-22 asset calls = %+v", executor.calls)
	}
	defaultPortAsset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "default-port", Host: stringPointerForInventory("prod.example"), Username: optionalString("ops")})
	if err != nil {
		t.Fatal(err)
	}
	defaultConnected := connectInventoryTestSession(t, manager, "owner-a", defaultPortAsset)
	executor.calls = nil
	noPort.SessionID = defaultConnected.ID
	dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", noPort, &result)
	if len(executor.calls) != 1 {
		t.Fatalf("no-port endpoint on default-port asset calls = %+v", executor.calls)
	}

	// The matching SSH port on the same host is allowed.
	executor.calls = nil
	sshRightPort := sshWrongPort
	sshRightPort.SessionID = jumpConnected.ID
	dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", sshRightPort, &result)
	if len(executor.calls) != 1 {
		t.Fatalf("ssh matching-port exec calls = %+v", executor.calls)
	}
}

func TestInventoryTargetExecDockerServerStaleSessionRequiresReconnect(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	manager := newInventorySessionManager(t)
	executor := &fakeInventoryDockerExecutor{result: docker.ExecResult{Output: "Linux\n"}}
	service := newInventoryService(database, nil, false, t.TempDir(),
		withInventoryWatchInterval(20*time.Millisecond), withInventoryDockerAccess(manager, executor))
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	runner := &recordingInventoryRunner{outputs: map[string][]byte{}}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")

	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "prod-host", Host: stringPointerForInventory("old.example"), Username: optionalString("ops")})
	if err != nil {
		t.Fatal(err)
	}
	stale := connectInventoryTestSession(t, manager, "owner-a", asset)
	newHost := "new.example"
	if _, err := database.AssetUpdate(ctx, asset.ID, store.AssetPatch{Host: store.Value(newHost)}); err != nil {
		t.Fatal(err)
	}

	// The live asset matches the new endpoint, but the connected session
	// still points at the old host: fail closed and require reconnect.
	request := inventoryDockerExecRequest()
	request.Target = map[string]any{"type": "context", "name": "prod", "dockerEndpoint": "ssh://ops@new.example"}
	request.SessionID = stale.ID
	response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", request, nil)
	requireInventoryIPCError(t, response, ipc.CodeForbidden)
	if len(executor.calls) != 0 {
		t.Fatalf("stale session exec reached the backend: %+v", executor.calls)
	}

	// After a manual reconnect the new session carries the new config and
	// the same request is allowed.
	if err := manager.Disconnect(stale.ID); err != nil {
		t.Fatal(err)
	}
	updated, err := database.AssetGetLive(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	reconnected := connectInventoryTestSession(t, manager, "owner-a", updated)
	request.SessionID = reconnected.ID
	var result inventoryTargetExecResponse
	dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", request, &result)
	if result.Output != "Linux\n" || len(executor.calls) != 1 || executor.calls[0].SessionID != reconnected.ID {
		t.Fatalf("reconnected exec = %+v calls=%+v", result, executor.calls)
	}
}

func TestInventoryTargetExecExitCodeContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	manager := newInventorySessionManager(t)
	executor := &fakeInventoryDockerExecutor{}
	service := newInventoryService(database, nil, false, t.TempDir(),
		withInventoryWatchInterval(20*time.Millisecond), withInventoryDockerAccess(manager, executor))
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	runner := &recordingInventoryRunner{outputs: map[string][]byte{}}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "prod-host", Host: stringPointerForInventory("prod.example"), Username: optionalString("ops")})
	if err != nil {
		t.Fatal(err)
	}
	connected := connectInventoryTestSession(t, manager, "owner-a", asset)

	requireExecFailure := func(response ipc.Response, exitCode int, output string) {
		t.Helper()
		requireInventoryIPCError(t, response, ipc.CodeIO)
		detailJSON, err := json.Marshal(response.Error.Detail)
		if err != nil {
			t.Fatal(err)
		}
		var detail inventoryTargetExecFailure
		if err := json.Unmarshal(detailJSON, &detail); err != nil {
			t.Fatal(err)
		}
		if detail.ExitCode != exitCode || detail.Output != output {
			t.Fatalf("failure detail = %+v, want exitCode=%d output=%q", detail, exitCode, output)
		}
	}

	// Non-zero exit with empty output is an executed-but-failed command.
	failing := inventoryDockerExecRequest()
	failing.SessionID = connected.ID
	executor.result = docker.ExecResult{Output: "", ExitCode: 7}
	response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", failing, nil)
	requireExecFailure(response, 7, "")
	rows, err := database.AuditQuery(ctx, store.AuditQuery{Limit: 10})
	if err != nil || len(rows) == 0 {
		t.Fatalf("audit rows = %+v, err=%v", rows, err)
	}
	if rows[0].ExitCode == nil || *rows[0].ExitCode != 7 {
		t.Fatalf("audit exit code = %v, want 7", rows[0].ExitCode)
	}

	// Non-zero exit with output preserves both.
	executor.result = docker.ExecResult{Output: "boom\n", ExitCode: 2}
	response = dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", failing, nil)
	requireExecFailure(response, 2, "boom\n")

	// Success carries exitCode 0.
	executor.result = docker.ExecResult{Output: "ok\n", ExitCode: 0}
	var result inventoryTargetExecResponse
	dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", failing, &result)
	if result.Output != "ok\n" || result.ExitCode != 0 {
		t.Fatalf("success result = %+v", result)
	}
}

func TestInventoryTargetExecLocalPathExitCodeContract(t *testing.T) {
	service, database := newInventoryTestService(t, true)
	runner := &recordingInventoryRunner{errors: map[string]error{
		"docker --context prod exec web uname -a": &fleetinventory.CommandExitError{Code: 3, Output: []byte("partial\n"), Stderr: "boom"},
	}}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	response := dispatchInventoryTest(t, dispatcher, context.Background(), "inventory_target_exec", inventoryDockerExecRequest(), nil)
	requireInventoryIPCError(t, response, ipc.CodeIO)
	detailJSON, err := json.Marshal(response.Error.Detail)
	if err != nil {
		t.Fatal(err)
	}
	var detail inventoryTargetExecFailure
	if err := json.Unmarshal(detailJSON, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ExitCode != 3 || detail.Output != "partial\n" {
		t.Fatalf("failure detail = %+v", detail)
	}
	rows, err := database.AuditQuery(context.Background(), store.AuditQuery{Limit: 10})
	if err != nil || len(rows) == 0 {
		t.Fatalf("audit rows = %+v, err=%v", rows, err)
	}
	if rows[0].ExitCode == nil || *rows[0].ExitCode != 3 {
		t.Fatalf("audit exit code = %v, want 3", rows[0].ExitCode)
	}
}

func requireInventoryExecCancelled(t *testing.T, response ipc.Response) {
	t.Helper()
	requireInventoryIPCError(t, response, ipc.CodeIO)
	detailJSON, err := json.Marshal(response.Error.Detail)
	if err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := json.Unmarshal(detailJSON, &detail); err != nil {
		t.Fatal(err)
	}
	if !detail.Cancelled {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestInventoryTargetExecCancelInFlight(t *testing.T) {
	service, _ := newInventoryTestService(t, true)
	runner := &recordingInventoryRunner{block: true, started: make(chan struct{}, 1)}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	execDone := make(chan ipc.Response, 1)
	go func() {
		execDone <- dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryDockerExecRequest(), nil)
	}()
	<-runner.started
	cancelResponse := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(inventoryDockerExecRequest()), nil)
	if !cancelResponse.OK {
		t.Fatalf("cancel response = %+v", cancelResponse)
	}
	requireInventoryExecCancelled(t, <-execDone)
}

func TestInventoryTargetExecCancelWithoutInFlightIsNoop(t *testing.T) {
	service, _ := newInventoryTestService(t, true)
	service.runner = &recordingInventoryRunner{outputs: map[string][]byte{}}
	dispatcher := newInventoryExecDispatcher(t, service)
	response := dispatchInventoryTest(t, dispatcher, context.Background(), "inventory_target_exec", inventoryCancelRequest("op-missing"), nil)
	if !response.OK {
		t.Fatalf("cancel response = %+v", response)
	}
}

func TestInventoryTargetExecCancelOwnerIsolation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	manager := newInventorySessionManager(t)
	executor := &fakeInventoryDockerExecutor{block: true, started: make(chan struct{}, 1)}
	service := newInventoryService(database, nil, false, t.TempDir(),
		withInventoryWatchInterval(20*time.Millisecond), withInventoryDockerAccess(manager, executor))
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	service.runner = &recordingInventoryRunner{outputs: map[string][]byte{}}
	dispatcher := newInventoryExecDispatcher(t, service)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	asset, err := database.AssetCreate(ctxA, store.AssetInput{Kind: "ssh", Name: "prod-host", Host: stringPointerForInventory("prod.example"), Username: optionalString("ops")})
	if err != nil {
		t.Fatal(err)
	}
	connected := connectInventoryTestSession(t, manager, "owner-a", asset)
	request := inventoryDockerExecRequest()
	request.SessionID = connected.ID
	execDone := make(chan ipc.Response, 1)
	go func() {
		execDone <- dispatchInventoryTest(t, dispatcher, ctxA, "inventory_target_exec", request, nil)
	}()
	<-executor.started
	// owner-b's cancel is an idempotent no-op and leaves owner-a's exec running.
	if response := dispatchInventoryTest(t, dispatcher, ctxB, "inventory_target_exec", inventoryCancelFor(request), nil); !response.OK {
		t.Fatalf("owner-b cancel = %+v", response)
	}
	select {
	case response := <-execDone:
		t.Fatalf("owner-a exec finished early: %+v", response)
	case <-time.After(50 * time.Millisecond):
	}
	if response := dispatchInventoryTest(t, dispatcher, ctxA, "inventory_target_exec", inventoryCancelFor(request), nil); !response.OK {
		t.Fatalf("owner-a cancel = %+v", response)
	}
	requireInventoryExecCancelled(t, <-execDone)
}

func TestInventoryTargetExecCancelSessionPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	manager := newInventorySessionManager(t)
	executor := &fakeInventoryDockerExecutor{block: true, started: make(chan struct{}, 1)}
	service := newInventoryService(database, nil, false, t.TempDir(),
		withInventoryWatchInterval(20*time.Millisecond), withInventoryDockerAccess(manager, executor))
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	service.runner = &recordingInventoryRunner{outputs: map[string][]byte{}}
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "prod-host", Host: stringPointerForInventory("prod.example"), Username: optionalString("ops")})
	if err != nil {
		t.Fatal(err)
	}
	connected := connectInventoryTestSession(t, manager, "owner-a", asset)
	request := inventoryDockerExecRequest()
	request.SessionID = connected.ID
	execDone := make(chan ipc.Response, 1)
	go func() {
		execDone <- dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", request, nil)
	}()
	<-executor.started
	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(request), nil); !response.OK {
		t.Fatalf("cancel response = %+v", response)
	}
	requireInventoryExecCancelled(t, <-execDone)
}

func TestInventoryTargetExecCancelBindsExactOperation(t *testing.T) {
	service, _ := newInventoryTestService(t, true)
	runner := &recordingInventoryRunner{block: true, started: make(chan struct{}, 2)}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	requestA := inventoryDockerExecRequest()
	requestA.OperationID = "op-a"
	requestB := inventoryDockerExecRequest()
	requestB.Target = map[string]any{"type": "context", "name": "other", "dockerEndpoint": "ssh://ops@other.example"}
	requestB.OperationID = "op-b"
	doneA := make(chan ipc.Response, 1)
	doneB := make(chan ipc.Response, 1)
	go func() { doneA <- dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", requestA, nil) }()
	go func() { doneB <- dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", requestB, nil) }()
	<-runner.started
	<-runner.started

	// Cancelling A must not touch B.
	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(requestA), nil); !response.OK {
		t.Fatalf("cancel A = %+v", response)
	}
	requireInventoryExecCancelled(t, <-doneA)
	select {
	case response := <-doneB:
		t.Fatalf("B was affected by A's cancel: %+v", response)
	case <-time.After(50 * time.Millisecond):
	}
	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(requestB), nil); !response.OK {
		t.Fatalf("cancel B = %+v", response)
	}
	requireInventoryExecCancelled(t, <-doneB)
}

func TestInventoryTargetExecCancelRetryNewOperationIsolated(t *testing.T) {
	service, _ := newInventoryTestService(t, true)
	runner := &recordingInventoryRunner{block: true, started: make(chan struct{}, 2)}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	requestA := inventoryDockerExecRequest()
	requestA.OperationID = "op-a"
	doneA := make(chan ipc.Response, 1)
	go func() { doneA <- dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", requestA, nil) }()
	<-runner.started
	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(requestA), nil); !response.OK {
		t.Fatalf("cancel A = %+v", response)
	}
	requireInventoryExecCancelled(t, <-doneA)

	// A late cancel for the settled A must not touch the new instance B,
	// and a duplicate cancel for A is an idempotent no-op.
	requestB := inventoryDockerExecRequest()
	requestB.OperationID = "op-b"
	doneB := make(chan ipc.Response, 1)
	go func() { doneB <- dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", requestB, nil) }()
	<-runner.started
	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(requestA), nil); !response.OK {
		t.Fatalf("late cancel A = %+v", response)
	}
	select {
	case response := <-doneB:
		t.Fatalf("B was affected by the late cancel for A: %+v", response)
	case <-time.After(50 * time.Millisecond):
	}
	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(requestA), nil); !response.OK {
		t.Fatalf("duplicate cancel A = %+v", response)
	}
	select {
	case response := <-doneB:
		t.Fatalf("B was affected by the duplicate cancel for A: %+v", response)
	case <-time.After(50 * time.Millisecond):
	}
	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(requestB), nil); !response.OK {
		t.Fatalf("cancel B = %+v", response)
	}
	requireInventoryExecCancelled(t, <-doneB)
}

func TestInventoryTargetExecCancelBeforeRegisterTombstones(t *testing.T) {
	service, _ := newInventoryTestService(t, true)
	runner := &recordingInventoryRunner{outputs: map[string][]byte{}}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	// The cancel overtakes the exec: it lands before the operation registers.
	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(inventoryDockerExecRequest()), nil); !response.OK {
		t.Fatalf("early cancel = %+v", response)
	}
	response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryDockerExecRequest(), nil)
	requireInventoryIPCError(t, response, ipc.CodeBadParam)
	if runner.callCount() != 0 {
		t.Fatalf("tombstoned operation started anyway: %+v", runner.calls)
	}
}

func TestInventoryTargetExecRejectsDuplicateOperationID(t *testing.T) {
	service, _ := newInventoryTestService(t, true)
	runner := &recordingInventoryRunner{block: true, started: make(chan struct{}, 1)}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	doneA := make(chan ipc.Response, 1)
	go func() {
		doneA <- dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryDockerExecRequest(), nil)
	}()
	<-runner.started
	response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryDockerExecRequest(), nil)
	requireInventoryIPCError(t, response, ipc.CodeBadParam)
	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(inventoryDockerExecRequest()), nil); !response.OK {
		t.Fatalf("cancel = %+v", response)
	}
	requireInventoryExecCancelled(t, <-doneA)
}

func TestInventoryTargetExecCancelMetadataMismatchNoops(t *testing.T) {
	service, _ := newInventoryTestService(t, true)
	runner := &recordingInventoryRunner{block: true, started: make(chan struct{}, 1)}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	doneA := make(chan ipc.Response, 1)
	go func() {
		doneA <- dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryDockerExecRequest(), nil)
	}()
	<-runner.started
	// A cancel whose launch metadata does not match the handle is a no-op.
	mismatched := inventoryDockerExecRequest()
	mismatched.Container = "other"
	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(mismatched), nil); !response.OK {
		t.Fatalf("mismatched cancel = %+v", response)
	}
	select {
	case response := <-doneA:
		t.Fatalf("exec cancelled by a metadata-mismatched cancel: %+v", response)
	case <-time.After(50 * time.Millisecond):
	}
	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(inventoryDockerExecRequest()), nil); !response.OK {
		t.Fatalf("cancel = %+v", response)
	}
	requireInventoryExecCancelled(t, <-doneA)
}

func TestInventoryTargetExecOperationIDValidation(t *testing.T) {
	service, _ := newInventoryTestService(t, true)
	service.runner = &recordingInventoryRunner{outputs: map[string][]byte{}}
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	empty := inventoryDockerExecRequest()
	empty.OperationID = ""
	requireInventoryIPCError(t, dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", empty, nil), ipc.CodeBadParam)
	overlong := inventoryDockerExecRequest()
	overlong.OperationID = strings.Repeat("x", 129)
	requireInventoryIPCError(t, dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", overlong, nil), ipc.CodeBadParam)
	cancel := inventoryCancelRequest("")
	requireInventoryIPCError(t, dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", cancel, nil), ipc.CodeBadParam)
}

func TestInventoryTargetExecSameIDTwoOwnersIndependent(t *testing.T) {
	service, _ := newInventoryTestService(t, true)
	runner := &recordingInventoryRunner{block: true, started: make(chan struct{}, 2)}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	requestA := inventoryDockerExecRequest()
	requestA.OperationID = "shared"
	requestB := inventoryDockerExecRequest()
	requestB.OperationID = "shared"
	doneA := make(chan ipc.Response, 1)
	doneB := make(chan ipc.Response, 1)
	go func() { doneA <- dispatchInventoryTest(t, dispatcher, ctxA, "inventory_target_exec", requestA, nil) }()
	go func() { doneB <- dispatchInventoryTest(t, dispatcher, ctxB, "inventory_target_exec", requestB, nil) }()
	<-runner.started
	<-runner.started

	// Each owner's cancel only reaches their own namespace.
	if response := dispatchInventoryTest(t, dispatcher, ctxB, "inventory_target_exec", inventoryCancelFor(requestB), nil); !response.OK {
		t.Fatalf("owner-b cancel = %+v", response)
	}
	requireInventoryExecCancelled(t, <-doneB)
	select {
	case response := <-doneA:
		t.Fatalf("owner-a exec was cancelled by owner-b: %+v", response)
	case <-time.After(50 * time.Millisecond):
	}
	if response := dispatchInventoryTest(t, dispatcher, ctxA, "inventory_target_exec", inventoryCancelFor(requestA), nil); !response.OK {
		t.Fatalf("owner-a cancel = %+v", response)
	}
	requireInventoryExecCancelled(t, <-doneA)
}

func TestInventoryTargetExecTombstoneFloodDoesNotEvictOtherOwner(t *testing.T) {
	service, _ := newInventoryTestService(t, true)
	service.runner = &recordingInventoryRunner{outputs: map[string][]byte{}}
	dispatcher := newInventoryExecDispatcher(t, service)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	requestA := inventoryDockerExecRequest()
	requestA.OperationID = "shared"
	if response := dispatchInventoryTest(t, dispatcher, ctxA, "inventory_target_exec", inventoryCancelFor(requestA), nil); !response.OK {
		t.Fatalf("owner-a cancel = %+v", response)
	}
	for index := 0; index < inventoryExecTombstoneLimit; index++ {
		flood := inventoryCancelRequest(fmt.Sprintf("op-flood-%d", index))
		if response := dispatchInventoryTest(t, dispatcher, ctxB, "inventory_target_exec", flood, nil); !response.OK {
			t.Fatalf("owner-b flood cancel %d = %+v", index, response)
		}
	}
	// owner-b's flood must not evict owner-a's tombstone for "shared".
	response := dispatchInventoryTest(t, dispatcher, ctxA, "inventory_target_exec", requestA, nil)
	requireInventoryIPCError(t, response, ipc.CodeBadParam)
}

func TestInventoryTargetExecDuplicateCancelKeepsTombstone(t *testing.T) {
	service, _ := newInventoryTestService(t, true)
	service.runner = &recordingInventoryRunner{outputs: map[string][]byte{}}
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	// Fill the owner's tombstone capacity; the oldest entry is op-0.
	for index := 0; index < inventoryExecTombstoneLimit; index++ {
		if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelRequest(fmt.Sprintf("op-%d", index)), nil); !response.OK {
			t.Fatalf("fill cancel %d = %+v", index, response)
		}
	}
	// Re-cancelling the oldest ID must not evict the tombstone just written.
	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelRequest("op-0"), nil); !response.OK {
		t.Fatalf("duplicate cancel = %+v", response)
	}
	request := inventoryDockerExecRequest()
	request.OperationID = "op-0"
	response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", request, nil)
	requireInventoryIPCError(t, response, ipc.CodeBadParam)
}

func TestInventoryTargetExecCancelMetadataMismatchNoTombstone(t *testing.T) {
	service, _ := newInventoryTestService(t, true)
	runner := &recordingInventoryRunner{block: true, started: make(chan struct{}, 1)}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	doneA := make(chan ipc.Response, 1)
	go func() {
		doneA <- dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryDockerExecRequest(), nil)
	}()
	<-runner.started
	// A cancel whose metadata does not match the handle is a no-op and
	// never tombstones the legitimate in-flight instance.
	mismatched := inventoryDockerExecRequest()
	mismatched.Container = "other"
	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(mismatched), nil); !response.OK {
		t.Fatalf("mismatched cancel = %+v", response)
	}
	select {
	case response := <-doneA:
		t.Fatalf("exec cancelled by a metadata-mismatched cancel: %+v", response)
	case <-time.After(50 * time.Millisecond):
	}
	service.execMu.Lock()
	_, tombstoned := service.execTombstones[inventoryExecKey{ownerID: "owner-a", operationID: inventoryDockerExecRequest().OperationID}]
	service.execMu.Unlock()
	if tombstoned {
		t.Fatal("metadata-mismatched cancel wrote a tombstone")
	}
	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(inventoryDockerExecRequest()), nil); !response.OK {
		t.Fatalf("cancel = %+v", response)
	}
	requireInventoryExecCancelled(t, <-doneA)
}

func TestInventoryTargetExecCancelKubernetesCanonicalTarget(t *testing.T) {
	service, _ := newInventoryTestService(t, true)
	runner := &recordingInventoryRunner{block: true, started: make(chan struct{}, 1)}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	requestA := inventoryKubernetesExecRequest()
	requestA.OperationID = "op-a"
	doneA := make(chan ipc.Response, 1)
	go func() { doneA <- dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", requestA, nil) }()
	<-runner.started

	// Same namespace/pod but a different context must not cancel.
	crossContext := inventoryKubernetesExecRequest()
	crossContext.OperationID = "op-a"
	crossContext.Target = map[string]any{"type": "pod", "context": "prod", "namespace": "ops", "pod": "api-0", "container": "app"}
	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(crossContext), nil); !response.OK {
		t.Fatalf("cross-context cancel = %+v", response)
	}
	select {
	case response := <-doneA:
		t.Fatalf("exec cancelled by a cross-context cancel: %+v", response)
	case <-time.After(50 * time.Millisecond):
	}

	// Same context/pod but a different effective container must not cancel.
	otherContainer := inventoryKubernetesExecRequest()
	otherContainer.OperationID = "op-a"
	otherContainer.Container = "sidecar"
	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(otherContainer), nil); !response.OK {
		t.Fatalf("other-container cancel = %+v", response)
	}
	select {
	case response := <-doneA:
		t.Fatalf("exec cancelled by a different-container cancel: %+v", response)
	case <-time.After(50 * time.Millisecond):
	}

	if response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryCancelFor(requestA), nil); !response.OK {
		t.Fatalf("cancel = %+v", response)
	}
	requireInventoryExecCancelled(t, <-doneA)
}

func TestInventoryTargetExecRejectsHighRiskAndInvalidRequests(t *testing.T) {
	service, _ := newInventoryTestService(t, true)
	runner := &recordingInventoryRunner{outputs: map[string][]byte{}}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	for _, action := range []string{"run", "cp", "context use", "build", "rmi", "push", "restart", ""} {
		request := inventoryDockerExecRequest()
		request.Action = action
		response := dispatchInventoryTest(t, dispatcher, context.Background(), "inventory_target_exec", request, nil)
		requireInventoryIPCError(t, response, ipc.CodeBadParam)
	}
	noContainer := inventoryDockerExecRequest()
	noContainer.Container = ""
	response := dispatchInventoryTest(t, dispatcher, context.Background(), "inventory_target_exec", noContainer, nil)
	requireInventoryIPCError(t, response, ipc.CodeBadParam)
	noCommand := inventoryDockerExecRequest()
	noCommand.Command = nil
	response = dispatchInventoryTest(t, dispatcher, context.Background(), "inventory_target_exec", noCommand, nil)
	requireInventoryIPCError(t, response, ipc.CodeBadParam)
	injection := inventoryDockerExecRequest()
	injection.Container = "--privileged"
	response = dispatchInventoryTest(t, dispatcher, context.Background(), "inventory_target_exec", injection, nil)
	requireInventoryIPCError(t, response, ipc.CodeBadParam)
	if runner.callCount() != 0 {
		t.Fatalf("rejected requests ran commands: %+v", runner.calls)
	}
}

func TestInventoryTargetExecDockerServerProductionConfirmation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	manager := newInventorySessionManager(t)
	executor := &fakeInventoryDockerExecutor{result: docker.ExecResult{Output: "Linux\n"}}
	service := newInventoryService(database, nil, false, t.TempDir(),
		withInventoryWatchInterval(20*time.Millisecond), withInventoryDockerAccess(manager, executor))
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	runner := &recordingInventoryRunner{outputs: map[string][]byte{}}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	importDockerContext(t, dispatcher, ctx, runner)

	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "prod-host", Host: stringPointerForInventory("prod.example"), Username: optionalString("ops")})
	if err != nil {
		t.Fatal(err)
	}
	tags := "env:production"
	if _, err := database.AssetUpdate(ctx, asset.ID, store.AssetPatch{Tags: &tags}); err != nil {
		t.Fatal(err)
	}
	connected := connectInventoryTestSession(t, manager, "owner-a", asset)
	request := inventoryDockerExecRequest()
	request.SessionID = connected.ID

	challengeFor := func(request inventoryTargetExecRequest) dockerConfirmationRequiredDetail {
		t.Helper()
		response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", request, nil)
		requireInventoryIPCError(t, response, ipc.CodeNeedsConfirm)
		if response.Error.Detail == nil {
			t.Fatal("confirmation detail missing")
		}
		detailJSON, err := json.Marshal(response.Error.Detail)
		if err != nil {
			t.Fatal(err)
		}
		var detail dockerConfirmationRequiredDetail
		if err := json.Unmarshal(detailJSON, &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Kind != dockerProductionConfirmationKind || detail.TargetSetHash == "" || detail.CommandHash == "" {
			t.Fatalf("confirmation detail = %+v", detail)
		}
		return detail
	}

	detail := challengeFor(request)
	if len(executor.calls) != 0 {
		t.Fatalf("unconfirmed exec reached the docker backend: %+v", executor.calls)
	}
	// Identical operations produce identical challenge hashes.
	if again := challengeFor(request); again.TargetSetHash != detail.TargetSetHash || again.CommandHash != detail.CommandHash {
		t.Fatalf("challenge hashes are not deterministic: %+v vs %+v", again, detail)
	}

	otherContainer := request
	otherContainer.Container = "other"
	replayed := otherContainer
	replayed.Confirmation = &dockerProductionConfirmation{Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash}
	response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", replayed, nil)
	requireInventoryIPCError(t, response, ipc.CodeNeedsConfirm)
	if len(executor.calls) != 0 {
		t.Fatalf("container-swapped replay reached the docker backend: %+v", executor.calls)
	}

	otherCommand := request
	otherCommand.Command = []string{"sh", "-c", "id"}
	replayed = otherCommand
	replayed.Confirmation = &dockerProductionConfirmation{Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash}
	response = dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", replayed, nil)
	requireInventoryIPCError(t, response, ipc.CodeNeedsConfirm)
	if len(executor.calls) != 0 {
		t.Fatalf("command-swapped replay reached the docker backend: %+v", executor.calls)
	}

	confirmed := request
	confirmed.Confirmation = &dockerProductionConfirmation{Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash, EnvironmentNames: detail.RequiredEnvironmentNames}
	var result inventoryTargetExecResponse
	dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", confirmed, &result)
	if result.Output != "Linux\n" {
		t.Fatalf("confirmed exec output = %q", result.Output)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("confirmed exec backend calls = %+v", executor.calls)
	}

	rows, err := database.AuditQuery(ctx, store.AuditQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var confirmationAudit, execAudit bool
	for _, row := range rows {
		if row.Kind == dockerProductionConfirmationKind {
			confirmationAudit = true
		}
		if row.Kind == "inventory_target_exec" {
			execAudit = true
		}
	}
	if !confirmationAudit || !execAudit {
		t.Fatalf("audit rows = %+v", rows)
	}
}

func TestInventoryTargetExecKubernetes(t *testing.T) {
	t.Run("desktop runs without authorization", func(t *testing.T) {
		service, _ := newInventoryTestService(t, true)
		runner := &recordingInventoryRunner{outputs: map[string][]byte{
			"kubectl --context staging exec -n ops api-0 -c app -- uname -a": []byte("Linux\n"),
		}}
		service.runner = runner
		dispatcher := newInventoryExecDispatcher(t, service)
		var result inventoryTargetExecResponse
		dispatchInventoryTest(t, dispatcher, context.Background(), "inventory_target_exec", inventoryKubernetesExecRequest(), &result)
		if result.Output != "Linux\n" {
			t.Fatalf("output = %q", result.Output)
		}
	})

	t.Run("server requires kubeconfig authorization", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		service, _ := newInventoryTestService(t, false)
		runner := &recordingInventoryRunner{outputs: map[string][]byte{}}
		service.runner = runner
		dispatcher := newInventoryExecDispatcher(t, service)
		response := dispatchInventoryTest(t, dispatcher, context.Background(), "inventory_target_exec", inventoryKubernetesExecRequest(), nil)
		requireInventoryIPCError(t, response, ipc.CodeForbidden)
		if runner.callCount() != 0 {
			t.Fatalf("denied exec ran commands: %+v", runner.calls)
		}
	})

	t.Run("server runs once authorized", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv(inventoryKubeconfigEnv, "1")
		service, _ := newInventoryTestService(t, false)
		runner := &recordingInventoryRunner{outputs: map[string][]byte{
			"kubectl --context staging exec -n ops api-0 -c app -- uname -a": []byte("Linux\n"),
		}}
		service.runner = runner
		dispatcher := newInventoryExecDispatcher(t, service)
		var result inventoryTargetExecResponse
		dispatchInventoryTest(t, dispatcher, context.Background(), "inventory_target_exec", inventoryKubernetesExecRequest(), &result)
		if result.Output != "Linux\n" || runner.callCount() != 1 {
			t.Fatalf("result = %+v calls = %+v", result, runner.calls)
		}
	})

	t.Run("server rejects high-risk actions", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv(inventoryKubeconfigEnv, "1")
		service, _ := newInventoryTestService(t, false)
		runner := &recordingInventoryRunner{outputs: map[string][]byte{}}
		service.runner = runner
		dispatcher := newInventoryExecDispatcher(t, service)
		for _, action := range []string{"run", "cp", "delete", "apply", ""} {
			request := inventoryKubernetesExecRequest()
			request.Action = action
			response := dispatchInventoryTest(t, dispatcher, context.Background(), "inventory_target_exec", request, nil)
			requireInventoryIPCError(t, response, ipc.CodeBadParam)
		}
		if runner.callCount() != 0 {
			t.Fatalf("rejected requests ran commands: %+v", runner.calls)
		}
	})

	t.Run("command failure surfaces the underlying error", func(t *testing.T) {
		service, _ := newInventoryTestService(t, true)
		runner := &recordingInventoryRunner{errors: map[string]error{
			"docker --context prod exec web uname -a": errors.New("container not running"),
		}}
		service.runner = runner
		dispatcher := newInventoryExecDispatcher(t, service)
		response := dispatchInventoryTest(t, dispatcher, context.Background(), "inventory_target_exec", inventoryDockerExecRequest(), nil)
		if response.OK {
			t.Fatal("failing exec was accepted")
		}
		if response.Error == nil || !strings.Contains(response.Error.Message, "container not running") {
			t.Fatalf("error = %+v", response.Error)
		}
	})
}

func newInventoryServiceForTest(t *testing.T, database *store.Store, desktop bool, dataDir string) *inventoryService {
	t.Helper()
	service := newInventoryService(database, nil, desktop, dataDir, withInventoryWatchInterval(20*time.Millisecond))
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	return service
}

func readPersistedWatchOwners(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}
	}
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Watches []struct {
			OwnerID string `json:"ownerId"`
			Path    string `json:"path"`
			GroupID string `json:"groupId"`
		} `json:"watches"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("watch store %s: %v", path, err)
	}
	owners := map[string]string{}
	for _, watch := range document.Watches {
		owners[watch.OwnerID] = watch.GroupID
	}
	return owners
}

func TestInventoryServerSSHWatchPersistsAndRestores(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dataDir := t.TempDir()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	sshDir := filepath.Join(home, ".ssh")
	pathA := filepath.Join(sshDir, "config-a")
	pathB := filepath.Join(sshDir, "config-b")
	writeInventoryTestFile(t, pathA, "Host alpha\n  HostName 10.0.0.1\n")
	writeInventoryTestFile(t, pathB, "Host bravo\n  HostName 10.0.0.2\n")
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")

	first := newInventoryServiceForTest(t, database, false, dataDir)
	dispatcher := newInventoryExecDispatcher(t, first)
	enabled := true
	var statusA inventorySSHWatchDTO
	dispatchInventoryTest(t, dispatcher, ctxA, "inventory_ssh_watch", inventorySSHWatchRequest{Enabled: &enabled, Path: pathA}, &statusA)
	if !statusA.Watching || statusA.LastResult == nil || statusA.LastResult.Created != 1 {
		t.Fatalf("owner-a watch = %+v", statusA)
	}
	var statusB inventorySSHWatchDTO
	dispatchInventoryTest(t, dispatcher, ctxB, "inventory_ssh_watch", inventorySSHWatchRequest{Enabled: &enabled, Path: pathB}, &statusB)
	if !statusB.Watching {
		t.Fatalf("owner-b watch = %+v", statusB)
	}
	watchFile := filepath.Join(dataDir, "inventory-ssh-watches.json")
	persisted := readPersistedWatchOwners(t, watchFile)
	if len(persisted) != 2 || persisted["owner-a"] == "" || persisted["owner-b"] == "" {
		t.Fatalf("persisted watches = %+v", persisted)
	}
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	second := newInventoryServiceForTest(t, database, false, dataDir)
	if err := second.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForInventory(t, func() (bool, error) {
		statusA := second.watchStatus(ctxA)
		statusB := second.watchStatus(ctxB)
		return statusA.Watching && statusA.LastResult != nil && statusB.Watching && statusB.LastResult != nil, nil
	})
	if rows, err := database.AssetList(ctxA, false); err != nil || len(rows) != 1 || rows[0].Name != "alpha" {
		t.Fatalf("restored owner-a assets = %+v, err=%v", rows, err)
	}

	disabled := false
	dispatcher2 := newInventoryExecDispatcher(t, second)
	dispatchInventoryTest(t, dispatcher2, ctxA, "inventory_ssh_watch", inventorySSHWatchRequest{Enabled: &disabled}, &statusA)
	if statusA.Watching {
		t.Fatal("owner-a watch still active after stop")
	}
	persisted = readPersistedWatchOwners(t, watchFile)
	if _, exists := persisted["owner-a"]; exists {
		t.Fatalf("stopped watch was persisted: %+v", persisted)
	}
	if _, exists := persisted["owner-b"]; !exists {
		t.Fatalf("owner-b watch lost with owner-a stop: %+v", persisted)
	}
	if status := second.watchStatus(ctxB); !status.Watching {
		t.Fatal("owner-b watch stopped with owner-a stop")
	}
}

func TestInventoryServerSSHWatchStartFailureIsNotPersisted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := t.TempDir()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := newInventoryServiceForTest(t, database, false, dataDir)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	broken := filepath.Join(home, ".ssh", "broken")
	if err := os.MkdirAll(broken, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := service.startWatch(ctx, broken, ""); err == nil {
		t.Fatal("watch over a directory was accepted")
	}
	if owners := readPersistedWatchOwners(t, filepath.Join(dataDir, "inventory-ssh-watches.json")); len(owners) != 0 {
		t.Fatalf("failed watch was persisted: %+v", owners)
	}
	if status := service.watchStatus(ctx); status.Watching {
		t.Fatal("failed watch is active")
	}
}

func TestInventoryDesktopWatchDoesNotPersist(t *testing.T) {
	dataDir := t.TempDir()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := newInventoryServiceForTest(t, database, true, dataDir)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	path := filepath.Join(t.TempDir(), "config")
	writeInventoryTestFile(t, path, "Host alpha\n  HostName 10.0.0.1\n")
	if _, err := service.startWatch(ctx, path, ""); err != nil {
		t.Fatal(err)
	}
	if status := service.watchStatus(ctx); !status.Watching {
		t.Fatal("desktop watch is not active")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "inventory-ssh-watches.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("desktop watch persisted to disk: %v", err)
	}
}

func TestInventoryServerWatchStoreRequiresDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := newInventoryService(database, nil, false, "")
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	path := filepath.Join(home, ".ssh", "config")
	writeInventoryTestFile(t, path, "Host alpha\n  HostName 10.0.0.1\n")
	if _, err := service.startWatch(ctx, path, ""); err == nil || !strings.Contains(err.Error(), "数据目录") {
		t.Fatalf("startWatch with an empty data dir must fail visibly: %v", err)
	}
	if status := service.watchStatus(ctx); status.Watching {
		t.Fatal("watch is active despite the missing data dir")
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("restore without a data dir must be a no-op: %v", err)
	}
	desktop := newInventoryService(database, nil, true, "")
	if _, err := desktop.startWatch(ctx, path, ""); err != nil {
		t.Fatalf("desktop inventory service must not require a data dir: %v", err)
	}
	_ = desktop.Shutdown(context.Background())
}

var _ fleetinventory.CommandRunner = (*recordingInventoryRunner)(nil)
