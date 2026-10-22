package inventory

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type recordedCommand struct {
	name string
	args []string
}

type fakeRunner struct {
	outputs map[string][]byte
	errors  map[string]error
	calls   []recordedCommand
}

func (r *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedCommand{name: name, args: append([]string(nil), args...)})
	key := name + " " + strings.Join(args, " ")
	if err := r.errors[key]; err != nil {
		return nil, err
	}
	return r.outputs[key], nil
}

func TestExecRunnerReturnsCommandExitError(t *testing.T) {
	runner := ExecRunner{}
	_, err := runner.Output(context.Background(), "sh", "-c", "printf partial; printf boom >&2; exit 7")
	var exitErr *CommandExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("err = %T %v", err, err)
	}
	if exitErr.Code != 7 || string(exitErr.Output) != "partial" || exitErr.Stderr != "boom" {
		t.Fatalf("exit error = %+v", exitErr)
	}
	if !strings.Contains(err.Error(), "exit code 7") {
		t.Fatalf("error message = %q", err.Error())
	}
	output, err := runner.Output(context.Background(), "sh", "-c", "printf ok")
	if err != nil || string(output) != "ok" {
		t.Fatalf("output = %q err = %v", output, err)
	}
}

func TestDiscoverDockerContextsReadOnlyParsing(t *testing.T) {
	runner := &fakeRunner{outputs: map[string][]byte{
		"docker context ls --format {{json .}}": []byte("{\"Name\":\"prod\",\"Description\":\"Production\",\"DockerEndpoint\":\"ssh://ops@prod.example\",\"KubernetesEndpoint\":\"\",\"ContextType\":\"moby\",\"Current\":true}\n" +
			"{\"Name\":\"local\",\"Description\":\"\",\"DockerEndpoint\":\"unix:///var/run/docker.sock\",\"ContextType\":\"moby\",\"Current\":false}\n"),
	}}
	candidates, err := DiscoverDockerContexts(context.Background(), runner)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []recordedCommand{{name: "docker", args: []string{"context", "ls", "--format", "{{json .}}"}}}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("commands = %+v, want %+v", runner.calls, wantCalls)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %+v", candidates)
	}
	local := candidates[0]
	if local.Key != "context/local" || local.Kind != DockerContextKind || local.Name != "local" || local.Host == nil || *local.Host != "unix:///var/run/docker.sock" {
		t.Fatalf("local candidate = %+v", local)
	}
	prodTarget, ok := candidates[1].Options["inventoryTarget"].(map[string]any)
	if !ok || prodTarget["current"] != true || prodTarget["dockerEndpoint"] != "ssh://ops@prod.example" {
		t.Fatalf("prod target = %+v", candidates[1].Options)
	}
}

func TestParseDockerContextsRejectsPartialInventory(t *testing.T) {
	for _, input := range []string{
		`{"Name":"valid","DockerEndpoint":"unix:///docker.sock"}` + "\nnot-json",
		`{"DockerEndpoint":"unix:///docker.sock"}`,
		`{"Name":"same"}` + "\n" + `{"Name":"same"}`,
	} {
		if _, err := ParseDockerContexts([]byte(input)); err == nil {
			t.Fatalf("invalid inventory was accepted: %q", input)
		}
	}
}

func TestDiscoverKubernetesContextsAndPodsReadOnly(t *testing.T) {
	runner := &fakeRunner{outputs: map[string][]byte{
		"kubectl config get-contexts -o json": []byte(`{"current-context":"staging","contexts":[{"name":"staging","context":{"cluster":"staging-cluster","user":"reader","namespace":"ops"}},{"name":"prod","context":{"cluster":"prod-cluster","user":"reader"}}]}`),
		"kubectl get pods -A -o json":         []byte(`{"items":[{"metadata":{"namespace":"ops","name":"api-0"},"spec":{"nodeName":"node-a","containers":[{"name":"app"},{"name":"sidecar"}]},"status":{"phase":"Running"}},{"metadata":{"namespace":"default","name":"worker-0"},"spec":{"containers":[{"name":"worker"}]},"status":{"phase":"Pending"}}]}`),
	}}
	candidates, err := DiscoverKubernetes(context.Background(), runner)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []recordedCommand{
		{name: "kubectl", args: []string{"config", "get-contexts", "-o", "json"}},
		{name: "kubectl", args: []string{"get", "pods", "-A", "-o", "json"}},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("commands = %+v, want %+v", runner.calls, wantCalls)
	}
	if len(candidates) != 4 {
		t.Fatalf("candidates = %+v", candidates)
	}
	var contextCandidate, podCandidate *Candidate
	for index := range candidates {
		candidate := &candidates[index]
		target := candidate.Options["inventoryTarget"].(map[string]any)
		switch target["type"] {
		case "context":
			if candidate.Name == "staging" {
				contextCandidate = candidate
			}
		case "pod":
			if candidate.Name == "ops/api-0" {
				podCandidate = candidate
			}
		}
		if candidate.Kind != KubernetesKind || candidate.Host != nil || candidate.AuthKind != nil {
			t.Fatalf("kubernetes candidate masquerades as a host: %+v", candidate)
		}
	}
	if contextCandidate == nil || contextCandidate.Key != "context/staging" {
		t.Fatalf("staging context = %+v", contextCandidate)
	}
	contextTarget := contextCandidate.Options["inventoryTarget"].(map[string]any)
	if contextTarget["current"] != true || contextTarget["cluster"] != "staging-cluster" {
		t.Fatalf("context target = %+v", contextTarget)
	}
	if podCandidate == nil || podCandidate.Key != "pod/staging/ops/api-0" {
		t.Fatalf("pod candidate = %+v", podCandidate)
	}
	podTarget := podCandidate.Options["inventoryTarget"].(map[string]any)
	if podTarget["context"] != "staging" || podTarget["container"] != "app" || podTarget["phase"] != "Running" {
		t.Fatalf("pod target = %+v", podTarget)
	}
	containers, ok := podTarget["containers"].([]string)
	if !ok || !reflect.DeepEqual(containers, []string{"app", "sidecar"}) {
		t.Fatalf("pod containers = %+v", podTarget["containers"])
	}
}

func TestKubernetesDiscoveryDoesNotReturnPartialCandidates(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string][]byte{
			"kubectl config get-contexts -o json": []byte(`{"current-context":"staging","contexts":[{"name":"staging","context":{"cluster":"cluster"}}]}`),
		},
		errors: map[string]error{"kubectl get pods -A -o json": errors.New("forbidden")},
	}
	candidates, err := DiscoverKubernetes(context.Background(), runner)
	if err == nil || candidates != nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
}

func TestParseKubernetesRejectsMissingIdentity(t *testing.T) {
	if _, _, err := ParseKubernetesContexts([]byte(`{"contexts":[{"context":{"cluster":"cluster"}}]}`)); err == nil {
		t.Fatal("context without a name was accepted")
	}
	if _, err := ParseKubernetesPods([]byte(`{"items":[{"metadata":{"name":"pod-without-namespace"}}]}`), "context"); err == nil {
		t.Fatal("pod without a namespace was accepted")
	}
}
