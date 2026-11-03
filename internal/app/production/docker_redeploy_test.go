//go:build darwin || linux

package production

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

type redeployCommandRunner struct {
	mu       sync.Mutex
	commands []string
	workDir  string
	// labelDir overrides only the working_dir label, keeping config_files stable.
	labelDir string
}

func (r *redeployCommandRunner) RunCommand(_ context.Context, command string) (docker.CommandResult, error) {
	r.mu.Lock()
	r.commands = append(r.commands, command)
	workDir, labelDir := r.workDir, r.labelDir
	r.mu.Unlock()
	if labelDir == "" {
		labelDir = workDir
	}
	switch command {
	case "docker 'ps' '-a' '--format' '{{json .}}'":
		return docker.CommandResult{Stdout: "{\"ID\":\"c1\",\"Names\":\"web\",\"Image\":\"example\",\"State\":\"running\",\"Status\":\"Up\",\"Labels\":\"com.docker.compose.project=shop,com.docker.compose.service=web\"}\n"}, nil
	case "docker 'inspect' 'c1'":
		raw, _ := json.Marshal(map[string]any{"Config": map[string]any{"Labels": map[string]string{
			"com.docker.compose.project":              "shop",
			"com.docker.compose.project.working_dir":  labelDir,
			"com.docker.compose.project.config_files": filepath.Join(workDir, "compose.yml"),
		}}})
		return docker.CommandResult{Stdout: string(raw)}, nil
	default:
		return docker.CommandResult{}, nil
	}
}

func (r *redeployCommandRunner) setLabelDir(dir string) {
	r.mu.Lock()
	r.labelDir = dir
	r.mu.Unlock()
}

func (r *redeployCommandRunner) upCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, command := range r.commands {
		if strings.Contains(command, "'up' '--detach'") {
			count++
		}
	}
	return count
}

type redeployIPCFixture struct {
	ctx        context.Context
	service    *terminalCommandService
	dispatcher *ipc.Dispatcher
	connected  *session.Session
	runner     *redeployCommandRunner
	path       string
}

func newRedeployIPCFixture(t *testing.T) *redeployIPCFixture {
	t.Helper()
	ctx := ipc.WithUserID(t.Context(), "redeploy-owner")
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: session.KindLocal, Name: "production", Tags: "env:prod"})
	if err != nil {
		t.Fatal(err)
	}
	connector := session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := session.NewManager(session.Config{Connector: connector})
	connected, err := manager.ConnectFor(ctx, session.Asset{ID: asset.ID, Name: asset.Name, Kind: session.KindLocal, StoreBacked: true})
	if err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	runner := &redeployCommandRunner{workDir: workDir}
	dockerService := docker.NewService(docker.StaticBackends{CommandBackend: docker.NewCommandBackend(runner, nil, docker.ShellPOSIX)})
	service := newTerminalCommandService(database, manager, dockerService, nil, nil, nil, nil, nil, false)
	dispatcher := ipc.NewDispatcher()
	if err := service.registerDockerMaintenance(dispatcher); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = dockerService.Close()
		_ = manager.Close()
		_ = database.Close()
	})
	return &redeployIPCFixture{
		ctx: ctx, service: service, dispatcher: dispatcher, connected: connected, runner: runner,
		path: filepath.Join(workDir, "compose.yml"),
	}
}

func (f *redeployIPCFixture) request(t *testing.T, base, content string) dockerComposeRedeployIPCRequest {
	t.Helper()
	stack, err := f.service.docker.ComposeStack(f.ctx, f.connected.ID, "shop")
	if err != nil || stack.UpCommand == "" {
		t.Fatalf("stack = %+v, %v", stack, err)
	}
	return dockerComposeRedeployIPCRequest{
		SessionID: f.connected.ID, Project: "shop", Path: f.path, ConfirmedCommand: stack.UpCommand,
		BaseContentBase64: base64.StdEncoding.EncodeToString([]byte(base)),
		ContentBase64:     base64.StdEncoding.EncodeToString([]byte(content)),
	}
}

func (f *redeployIPCFixture) write(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(f.path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *redeployIPCFixture) content(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestComposeRedeployIPCConfirmsActualBytesBeforeWrite(t *testing.T) {
	fixture := newRedeployIPCFixture(t)
	fixture.write(t, "old\n")
	request := fixture.request(t, "old\n", "new\n")
	response := dispatchExecBatchTest(fixture.dispatcher, fixture.ctx, "docker_compose_redeploy_prepare", request)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("unconfirmed response = %+v", response)
	}
	if got := fixture.content(t); got != "old\n" || fixture.runner.upCalls() != 0 {
		t.Fatalf("unconfirmed write/up: content=%q up=%d", got, fixture.runner.upCalls())
	}
	request.ProductionConfirmation = &dockerProductionConfirmation{Confirmed: true, TargetSetHash: "wrong", CommandHash: "wrong"}
	response = dispatchExecBatchTest(fixture.dispatcher, fixture.ctx, "docker_compose_redeploy_prepare", request)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNeedsConfirm || fixture.content(t) != "old\n" {
		t.Fatalf("wrong confirmation response = %+v content=%q", response, fixture.content(t))
	}
	var challenge dockerConfirmationRequiredDetail
	encoded, err := json.Marshal(response.Error.Detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &challenge); err != nil {
		t.Fatal(err)
	}
	request.ProductionConfirmation = &dockerProductionConfirmation{
		Confirmed: true, TargetSetHash: challenge.TargetSetHash, CommandHash: challenge.CommandHash,
		EnvironmentNames: challenge.RequiredEnvironmentNames,
	}
	response = dispatchExecBatchTest(fixture.dispatcher, fixture.ctx, "docker_compose_redeploy_prepare", request)
	if !response.OK {
		t.Fatalf("confirmed prepare = %+v", response)
	}
	prepared := decodeExecBatchTest[dockerComposeRedeployPrepareResult](t, response)
	if prepared.Ticket == "" || prepared.ContentHash != dockerConfirmationDigest("content-v1", []byte("new\n")) {
		t.Fatalf("prepared result = %+v", prepared)
	}
	if got := fixture.content(t); got != "new\n" || fixture.runner.upCalls() != 0 {
		t.Fatalf("confirmed write/up: content=%q up=%d", got, fixture.runner.upCalls())
	}
	attachRequest := request
	attachRequest.Ticket = prepared.Ticket
	_, _, ticket, err := fixture.service.authorizeComposeRedeployAttach(fixture.ctx, attachRequest)
	if err != nil || ticket.operation.ContentHash != prepared.ContentHash {
		t.Fatalf("ticket attach = %+v, %v", ticket, err)
	}
	if _, _, _, err := fixture.service.authorizeComposeRedeployAttach(fixture.ctx, attachRequest); err == nil {
		t.Fatal("redeploy ticket was accepted twice")
	}

	stale := fixture.request(t, "old\n", "must-not-win\n")
	response = dispatchExecBatchTest(fixture.dispatcher, fixture.ctx, "docker_compose_redeploy_prepare", stale)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam || fixture.content(t) != "new\n" {
		t.Fatalf("stale base response = %+v content=%q", response, fixture.content(t))
	}

	changed := fixture.request(t, "new\n", "confirmed-new\n")
	response = dispatchExecBatchTest(fixture.dispatcher, fixture.ctx, "docker_compose_redeploy_prepare", changed)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("changed content did not require production confirmation: %+v", response)
	}
	encoded, err = json.Marshal(response.Error.Detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &challenge); err != nil {
		t.Fatal(err)
	}
	fixture.write(t, "external\n")
	changed.ProductionConfirmation = &dockerProductionConfirmation{
		Confirmed: true, TargetSetHash: challenge.TargetSetHash, CommandHash: challenge.CommandHash,
		EnvironmentNames: challenge.RequiredEnvironmentNames,
	}
	response = dispatchExecBatchTest(fixture.dispatcher, fixture.ctx, "docker_compose_redeploy_prepare", changed)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam || fixture.content(t) != "external\n" {
		t.Fatalf("changed base response = %+v content=%q", response, fixture.content(t))
	}
	if fixture.runner.upCalls() != 0 {
		t.Fatalf("compose up ran during prepare tests: %d", fixture.runner.upCalls())
	}
}

func TestDockerConfirmationAuditOmitsPruneCapabilities(t *testing.T) {
	target := dockerConfirmationTarget{SessionID: "s1", AssetID: "asset-1", Environment: "production"}
	operation := dockerMutationOperation{Kind: "prune", PreviewToken: "secret-preview-token", PreviewDigest: "secret-preview-digest"}
	challenge, err := newDockerProductionChallenge(target, operation, builtInProductionClassifier)
	if err != nil {
		t.Fatal(err)
	}
	input := newDockerConfirmationAuditInput(ipc.WithUserID(t.Context(), "owner"), target, challenge, operation, 123)
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, operation.PreviewToken) || strings.Contains(text, operation.PreviewDigest) {
		t.Fatalf("audit contains prune capabilities: %s", text)
	}
	if !strings.Contains(text, challenge.commandHash) || !strings.Contains(text, "prune") {
		t.Fatalf("audit lost operation hashes: %s", text)
	}
}

func (f *redeployIPCFixture) setBackupOption(t *testing.T, enabled bool) {
	t.Helper()
	options := `{"backupBeforeOverwrite":` + strconv.FormatBool(enabled) + `}`
	if _, err := f.service.database.AssetUpdate(f.ctx, f.connected.Asset().ID, store.AssetPatch{OptionsJSON: &options}); err != nil {
		t.Fatal(err)
	}
}

func (f *redeployIPCFixture) confirmedPrepare(t *testing.T, request dockerComposeRedeployIPCRequest) {
	t.Helper()
	response := dispatchExecBatchTest(f.dispatcher, f.ctx, "docker_compose_redeploy_prepare", request)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("expected a confirmation challenge: %+v", response)
	}
	var challenge dockerConfirmationRequiredDetail
	encoded, err := json.Marshal(response.Error.Detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &challenge); err != nil {
		t.Fatal(err)
	}
	request.ProductionConfirmation = &dockerProductionConfirmation{
		Confirmed: true, TargetSetHash: challenge.TargetSetHash, CommandHash: challenge.CommandHash,
		EnvironmentNames: challenge.RequiredEnvironmentNames,
	}
	if response = dispatchExecBatchTest(f.dispatcher, f.ctx, "docker_compose_redeploy_prepare", request); !response.OK {
		t.Fatalf("confirmed prepare = %+v", response)
	}
}

func TestComposeRedeployBackupFollowsStoredAssetOption(t *testing.T) {
	fixture := newRedeployIPCFixture(t)
	backupPath := fixture.path + ".nexterm-bak"
	readBackup := func() (string, bool) {
		data, err := os.ReadFile(backupPath)
		return string(data), err == nil
	}

	// Default: the asset has no option, so no backup is requested.
	fixture.write(t, "v1\n")
	fixture.confirmedPrepare(t, fixture.request(t, "v1\n", "v2\n"))
	if _, ok := readBackup(); ok || fixture.content(t) != "v2\n" {
		t.Fatalf("default overwrite produced a backup or lost the write: content=%q", fixture.content(t))
	}

	// Enabling the option after the session connected applies without reconnecting.
	fixture.setBackupOption(t, true)
	fixture.confirmedPrepare(t, fixture.request(t, "v2\n", "v3\n"))
	if got, ok := readBackup(); !ok || got != "v2\n" || fixture.content(t) != "v3\n" {
		t.Fatalf("enabled overwrite: backup=%q ok=%v content=%q", got, ok, fixture.content(t))
	}

	// Disabling again stops further backups: the stale backup is not refreshed.
	fixture.setBackupOption(t, false)
	fixture.confirmedPrepare(t, fixture.request(t, "v3\n", "v4\n"))
	if got, _ := readBackup(); got != "v2\n" || fixture.content(t) != "v4\n" {
		t.Fatalf("disabled overwrite: backup=%q content=%q", got, fixture.content(t))
	}
}

func (f *redeployIPCFixture) confirmed(t *testing.T, request dockerComposeRedeployIPCRequest) dockerComposeRedeployIPCRequest {
	t.Helper()
	response := dispatchExecBatchTest(f.dispatcher, f.ctx, "docker_compose_redeploy_prepare", request)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("expected confirmation challenge: %+v", response)
	}
	var challenge dockerConfirmationRequiredDetail
	encoded, err := json.Marshal(response.Error.Detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &challenge); err != nil {
		t.Fatal(err)
	}
	request.ProductionConfirmation = &dockerProductionConfirmation{
		Confirmed: true, TargetSetHash: challenge.TargetSetHash, CommandHash: challenge.CommandHash,
		EnvironmentNames: challenge.RequiredEnvironmentNames,
	}
	return request
}

func TestComposeRedeployPrepareReturnsAndBindsCommand(t *testing.T) {
	fixture := newRedeployIPCFixture(t)
	fixture.write(t, "old\n")
	request := fixture.request(t, "old\n", "new\n")
	stack, err := fixture.service.docker.ComposeStack(fixture.ctx, fixture.connected.ID, "shop")
	if err != nil || stack.UpCommand == "" {
		t.Fatalf("stack = %+v, %v", stack, err)
	}

	// A mismatched confirmed command is refused before anything is written.
	mismatch := fixture.confirmed(t, request)
	mismatch.ConfirmedCommand = stack.UpCommand + " --other"
	response := dispatchExecBatchTest(fixture.dispatcher, fixture.ctx, "docker_compose_redeploy_prepare", mismatch)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam || fixture.content(t) != "old\n" {
		t.Fatalf("mismatched command response = %+v content=%q", response, fixture.content(t))
	}

	request = fixture.confirmed(t, request)
	request.ConfirmedCommand = stack.UpCommand
	response = dispatchExecBatchTest(fixture.dispatcher, fixture.ctx, "docker_compose_redeploy_prepare", request)
	if !response.OK {
		t.Fatalf("prepare = %+v", response)
	}
	prepared := decodeExecBatchTest[dockerComposeRedeployPrepareResult](t, response)
	if prepared.Command != stack.UpCommand {
		t.Fatalf("command = %q, want %q", prepared.Command, stack.UpCommand)
	}

	// The stack changes after prepare: attach must be refused without running compose.
	fixture.runner.setLabelDir(t.TempDir())
	attach := request
	attach.Ticket = prepared.Ticket
	_, _, ticket, err := fixture.service.authorizeComposeRedeployAttach(fixture.ctx, attach)
	if err != nil || ticket.command != stack.UpCommand {
		t.Fatalf("ticket = %+v, %v", ticket, err)
	}
	_, err = fixture.service.docker.AttachCompose(fixture.ctx, docker.ComposeAttachRequest{
		SessionID: fixture.connected.ID, Project: "shop", Action: docker.ComposeActionUp,
		ExpectedCommand: ticket.command, Sink: docker.FrameSinkFunc(func(context.Context, []byte) error { return nil }),
	})
	if !errors.Is(err, docker.ErrComposeChanged) || fixture.runner.upCalls() != 0 {
		t.Fatalf("attach err = %v, up calls = %d", err, fixture.runner.upCalls())
	}
}

func TestDockerMutationCommandHashFollowsUpCommand(t *testing.T) {
	first := dockerMutationOperation{Kind: "compose_redeploy", CommandHash: dockerConfirmationDigest("compose-up-v1", []byte("a"))}
	second := first
	second.CommandHash = dockerConfirmationDigest("compose-up-v1", []byte("b"))
	target := dockerConfirmationTarget{SessionID: "s1", Environment: "prod"}
	one, err := newDockerProductionChallenge(target, first, func(string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	two, err := newDockerProductionChallenge(target, second, func(string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if one == nil || two == nil || one.commandHash == two.commandHash {
		t.Fatalf("command hash did not change: %+v %+v", one, two)
	}
}

func TestComposeRedeployPrepareRequiresConfirmedCommand(t *testing.T) {
	fixture := newRedeployIPCFixture(t)
	fixture.write(t, "old\n")
	request := fixture.request(t, "old\n", "new\n")
	request.ConfirmedCommand = ""
	request.ProductionConfirmation = &dockerProductionConfirmation{Confirmed: true}
	response := dispatchExecBatchTest(fixture.dispatcher, fixture.ctx, "docker_compose_redeploy_prepare", request)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam || fixture.content(t) != "old\n" {
		t.Fatalf("missing confirmedCommand response = %+v content=%q", response, fixture.content(t))
	}
}
