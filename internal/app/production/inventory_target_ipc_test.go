package production

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fleetinventory "github.com/Hello-CTF/NexTerm/internal/fleet/inventory"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type recordingInventoryRunner struct {
	calls   [][]string
	outputs map[string][]byte
	errors  map[string]error
}

func (r *recordingInventoryRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	key := name + " " + strings.Join(args, " ")
	if err := r.errors[key]; err != nil {
		return nil, err
	}
	return r.outputs[key], nil
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

func inventoryDockerExecRequest() inventoryTargetExecRequest {
	return inventoryTargetExecRequest{
		Kind:      "docker-context",
		Target:    map[string]any{"type": "context", "name": "prod"},
		Action:    "exec",
		Container: "web",
		Command:   []string{"uname", "-a"},
	}
}

func inventoryKubernetesExecRequest() inventoryTargetExecRequest {
	return inventoryTargetExecRequest{
		Kind: "kubernetes",
		Target: map[string]any{
			"type": "pod", "context": "staging", "namespace": "ops", "pod": "api-0", "container": "app",
		},
		Action:  "exec",
		Command: []string{"uname", "-a"},
	}
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
	if len(runner.calls) != 0 {
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
	if len(runner.calls) != 1 || strings.Join(runner.calls[0], " ") != strings.Join(want, " ") {
		t.Fatalf("calls = %+v, want %+v", runner.calls, want)
	}
	rows, err := database.AuditQuery(context.Background(), store.AuditQuery{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Kind != "inventory_target_exec" {
		t.Fatalf("exec audit rows = %+v, err=%v", rows, err)
	}
}

func TestInventoryTargetExecDockerServerRequiresOwnedImport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	service, _ := newInventoryTestService(t, false)
	runner := &recordingInventoryRunner{outputs: map[string][]byte{}}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")

	response := dispatchInventoryTest(t, dispatcher, ctxA, "inventory_target_exec", inventoryDockerExecRequest(), nil)
	requireInventoryIPCError(t, response, ipc.CodeForbidden)
	if len(runner.calls) != 0 {
		t.Fatalf("unauthorized exec ran commands: %+v", runner.calls)
	}

	importDockerContext(t, dispatcher, ctxA, runner)
	runner.outputs["docker --context prod exec web uname -a"] = []byte("Linux\n")
	var result inventoryTargetExecResponse
	dispatchInventoryTest(t, dispatcher, ctxA, "inventory_target_exec", inventoryDockerExecRequest(), &result)
	if result.Output != "Linux\n" {
		t.Fatalf("owner exec output = %q", result.Output)
	}

	before := len(runner.calls)
	response = dispatchInventoryTest(t, dispatcher, ctxB, "inventory_target_exec", inventoryDockerExecRequest(), nil)
	requireInventoryIPCError(t, response, ipc.CodeForbidden)
	if len(runner.calls) != before {
		t.Fatalf("cross-owner exec ran commands: %+v", runner.calls[before:])
	}
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
	if len(runner.calls) != 0 {
		t.Fatalf("rejected requests ran commands: %+v", runner.calls)
	}
}

func TestInventoryTargetExecDockerServerProductionConfirmation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	service, database := newInventoryTestService(t, false)
	runner := &recordingInventoryRunner{outputs: map[string][]byte{}}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	importDockerContext(t, dispatcher, ctx, runner)

	asset, found, err := inventoryAssetByName(database, ctx, "prod")
	if err != nil || !found {
		t.Fatalf("imported asset = %+v, found=%v, err=%v", asset, found, err)
	}
	tags := "env:production"
	if _, err := database.AssetUpdate(ctx, asset.ID, store.AssetPatch{Tags: &tags}); err != nil {
		t.Fatal(err)
	}

	response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", inventoryDockerExecRequest(), nil)
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
	if len(runner.calls) != 1 {
		t.Fatalf("unconfirmed exec ran commands: %+v", runner.calls)
	}

	wrong := inventoryDockerExecRequest()
	wrong.Confirmation = &dockerProductionConfirmation{Confirmed: true, TargetSetHash: "wrong", CommandHash: "wrong"}
	response = dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", wrong, nil)
	requireInventoryIPCError(t, response, ipc.CodeNeedsConfirm)

	runner.outputs["docker --context prod exec web uname -a"] = []byte("Linux\n")
	confirmed := inventoryDockerExecRequest()
	confirmed.Confirmation = &dockerProductionConfirmation{Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash}
	var result inventoryTargetExecResponse
	dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", confirmed, &result)
	if result.Output != "Linux\n" {
		t.Fatalf("confirmed exec output = %q", result.Output)
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
		if len(runner.calls) != 0 {
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
		if result.Output != "Linux\n" || len(runner.calls) != 1 {
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
		if len(runner.calls) != 0 {
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
