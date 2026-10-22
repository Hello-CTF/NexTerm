package production

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func builtInProductionClassifier(environment string) (bool, error) {
	return guard.IsBuiltInProductionEnvironment(environment), nil
}

func TestDockerProductionGuardTriggeredByEnvProdAndBindsOperation(t *testing.T) {
	environment := store.AssetEnvironment(store.AssetRow{Tags: "team:ops, env:prod"})
	target := dockerConfirmationTarget{SessionID: "s1", SessionName: "production", Environment: environment}
	operation := dockerMutationOperation{Kind: "compose_redeploy", Project: "shop", Path: "/srv/shop/compose.yml", ContentHash: strings.Repeat("a", 64)}
	challenge, err := newDockerProductionChallenge(target, operation, builtInProductionClassifier)
	if err != nil {
		t.Fatal(err)
	}
	if challenge == nil {
		t.Fatal("env:prod did not produce a confirmation challenge")
	}
	if len(challenge.requiredEnvironmentNames) != 1 || challenge.requiredEnvironmentNames[0] != "production" {
		t.Fatalf("required environment names = %v", challenge.requiredEnvironmentNames)
	}
	if err := validateDockerProductionConfirmation(nil, challenge); err == nil {
		t.Fatal("unconfirmed production operation was accepted")
	}
	confirmation := &dockerProductionConfirmation{
		Confirmed: true, TargetSetHash: challenge.targetSetHash, CommandHash: challenge.commandHash,
		EnvironmentNames: []string{"production"},
	}
	if err := validateDockerProductionConfirmation(confirmation, challenge); err != nil {
		t.Fatalf("valid confirmation rejected: %v", err)
	}
	wrongEnvironment := &dockerProductionConfirmation{
		Confirmed: true, TargetSetHash: challenge.targetSetHash, CommandHash: challenge.commandHash,
		EnvironmentNames: []string{"staging"},
	}
	if err := validateDockerProductionConfirmation(wrongEnvironment, challenge); err == nil {
		t.Fatal("wrong environment name accepted")
	}
	missingEnvironment := &dockerProductionConfirmation{
		Confirmed: true, TargetSetHash: challenge.targetSetHash, CommandHash: challenge.commandHash,
	}
	if err := validateDockerProductionConfirmation(missingEnvironment, challenge); err == nil {
		t.Fatal("missing environment name accepted")
	}
	cancelled := &dockerProductionConfirmation{
		Confirmed: false, TargetSetHash: challenge.targetSetHash, CommandHash: challenge.commandHash,
		EnvironmentNames: []string{"production"},
	}
	if err := validateDockerProductionConfirmation(cancelled, challenge); err == nil {
		t.Fatal("cancelled confirmation accepted")
	}
	changed, err := newDockerProductionChallenge(target, dockerMutationOperation{Kind: "prune", PreviewToken: "other-token", PreviewDigest: "other-digest"}, builtInProductionClassifier)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDockerProductionConfirmation(confirmation, changed); err == nil {
		t.Fatal("confirmation was replayed for a different operation")
	}
}

func TestDockerProductionGuardSkipsNonProductionAndReturnsStructuredError(t *testing.T) {
	target := dockerConfirmationTarget{SessionID: "s1", Environment: "staging"}
	operation := dockerMutationOperation{Kind: "compose_down", Project: "shop"}
	if challenge, err := newDockerProductionChallenge(target, operation, builtInProductionClassifier); err != nil || challenge != nil {
		t.Fatalf("staging operation challenge = %+v, %v", challenge, err)
	}
	target.Environment = "production"
	challenge, err := newDockerProductionChallenge(target, operation, builtInProductionClassifier)
	if err != nil {
		t.Fatal(err)
	}
	err = validateDockerProductionConfirmation(nil, challenge)
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
	var detail dockerConfirmationRequiredDetail
	if err := json.Unmarshal(encoded, &appErr); err != nil {
		t.Fatal(err)
	}
	encodedDetail, err := json.Marshal(appErr.Detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encodedDetail, &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.RequiredEnvironmentNames) != 1 || detail.RequiredEnvironmentNames[0] != "production" {
		t.Fatalf("detail required environment names = %v", detail.RequiredEnvironmentNames)
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
