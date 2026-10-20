package production

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestDockerProductionGuardTriggeredByEnvProdAndBindsOperation(t *testing.T) {
	environment := store.AssetEnvironment(store.AssetRow{Tags: "team:ops, env:prod"})
	target := dockerConfirmationTarget{SessionID: "s1", SessionName: "production", Environment: environment}
	operation := dockerMutationOperation{Kind: "compose_redeploy", Project: "shop", Path: "/srv/shop/compose.yml", ContentHash: strings.Repeat("a", 64)}
	challenge := newDockerProductionChallenge(target, operation)
	if challenge == nil {
		t.Fatal("env:prod did not produce a confirmation challenge")
	}
	if err := validateDockerProductionConfirmation(nil, challenge); err == nil {
		t.Fatal("unconfirmed production operation was accepted")
	}
	confirmation := &dockerProductionConfirmation{Confirmed: true, TargetSetHash: challenge.targetSetHash, CommandHash: challenge.commandHash}
	if err := validateDockerProductionConfirmation(confirmation, challenge); err != nil {
		t.Fatalf("valid confirmation rejected: %v", err)
	}
	changed := newDockerProductionChallenge(target, dockerMutationOperation{Kind: "prune", PreviewToken: "other-token", PreviewDigest: "other-digest"})
	if err := validateDockerProductionConfirmation(confirmation, changed); err == nil {
		t.Fatal("confirmation was replayed for a different operation")
	}
}

func TestDockerProductionGuardSkipsNonProductionAndReturnsStructuredError(t *testing.T) {
	target := dockerConfirmationTarget{SessionID: "s1", Environment: "staging"}
	operation := dockerMutationOperation{Kind: "compose_down", Project: "shop"}
	if challenge := newDockerProductionChallenge(target, operation); challenge != nil {
		t.Fatal("staging operation requires confirmation")
	}
	target.Environment = "production"
	challenge := newDockerProductionChallenge(target, operation)
	err := validateDockerProductionConfirmation(nil, challenge)
	encoded, marshalErr := json.Marshal(dockerMutationIPCError(err))
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var appErr ipc.Error
	if marshalErr := json.Unmarshal(encoded, &appErr); marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if appErr.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("error = %s", encoded)
	}
	if strings.Contains(string(encoded), "compose_down") || strings.Contains(string(encoded), "shop") {
		t.Fatalf("error detail leaked operation plaintext: %s", encoded)
	}
}

func TestComposeRedeployPathMustComeFromStack(t *testing.T) {
	stack := docker.ComposeStack{WorkingDir: "/srv/shop", ConfigFiles: []string{"/srv/shop/compose.yml"}, EnvPath: "/srv/shop/.env"}
	if !composeEditPathAllowed(stack, stack.ConfigFiles[0]) || !composeEditPathAllowed(stack, stack.EnvPath) {
		t.Fatal("stack file was rejected")
	}
	if composeEditPathAllowed(stack, "/etc/passwd") || composeEditPathAllowed(stack, "") {
		t.Fatal("unrelated path was accepted")
	}
	if !validDockerContentHash(strings.Repeat("0a", 32)) || validDockerContentHash("not-a-hash") {
		t.Fatal("content hash validation is inconsistent")
	}
}
