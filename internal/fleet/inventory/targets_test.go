package inventory

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func dockerContextCandidate(name string) Candidate {
	return Candidate{
		Key: "context/" + name, Kind: DockerContextKind, Name: name,
		Options: map[string]any{"inventoryTarget": map[string]any{"type": "context", "name": name}},
	}
}

func kubernetesPodCandidate(contextName, namespace, pod, container string) Candidate {
	return Candidate{
		Key: "pod/" + contextName + "/" + namespace + "/" + pod, Kind: KubernetesKind, Name: namespace + "/" + pod,
		Options: map[string]any{"inventoryTarget": map[string]any{
			"type": "pod", "context": contextName, "namespace": namespace, "pod": pod, "container": container,
		}},
	}
}

func TestResolveTargetKinds(t *testing.T) {
	dockerTarget, err := ResolveTarget(dockerContextCandidate("prod"))
	if err != nil {
		t.Fatal(err)
	}
	if dockerTarget.Docker == nil || dockerTarget.Docker.Name != "prod" || dockerTarget.Kind != DockerContextKind {
		t.Fatalf("docker target = %+v", dockerTarget)
	}
	podTarget, err := ResolveTarget(kubernetesPodCandidate("staging", "ops", "api-0", "app"))
	if err != nil {
		t.Fatal(err)
	}
	kubernetes := podTarget.Kubernetes
	if kubernetes == nil || kubernetes.Context != "staging" || kubernetes.Namespace != "ops" || kubernetes.Pod != "api-0" || kubernetes.Container != "app" {
		t.Fatalf("kubernetes target = %+v", podTarget)
	}
	contextTarget, err := ResolveTarget(Candidate{
		Key: "context/staging", Kind: KubernetesKind, Name: "staging",
		Options: map[string]any{"inventoryTarget": map[string]any{"type": "context", "name": "staging"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if contextTarget.Kubernetes == nil || contextTarget.Kubernetes.Pod != "" {
		t.Fatalf("kubernetes context target = %+v", contextTarget)
	}
}

func TestResolveTargetRejectsInvalidCandidates(t *testing.T) {
	for name, candidate := range map[string]Candidate{
		"no options":     {Key: "host/a", Kind: "ssh", Name: "a"},
		"invalid option": {Key: "context/a", Kind: DockerContextKind, Name: "a", Options: map[string]any{"inventoryTarget": "context"}},
		"wrong type":     {Key: "context/a", Kind: DockerContextKind, Name: "a", Options: map[string]any{"inventoryTarget": map[string]any{"type": "pod"}}},
		"unknown kind":   {Key: "host/a", Kind: "ssh", Name: "a", Options: map[string]any{"inventoryTarget": map[string]any{"type": "context", "name": "a"}}},
	} {
		if _, err := ResolveTarget(candidate); err == nil {
			t.Fatalf("%s candidate was accepted: %+v", name, candidate)
		}
	}
}

func TestDockerContextTargetExecCommand(t *testing.T) {
	target := Target{Kind: DockerContextKind, Docker: &DockerContextTarget{Name: "prod"}}
	name, args, err := target.ExecCommand(TargetExecAction{Container: "web", Command: []string{"sh", "-c", "uptime"}})
	if err != nil {
		t.Fatal(err)
	}
	if name != "docker" {
		t.Fatalf("binary = %q", name)
	}
	want := []string{"--context", "prod", "exec", "web", "sh", "-c", "uptime"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %+v, want %+v", args, want)
	}
}

func TestKubernetesPodTargetExecCommand(t *testing.T) {
	target := Target{Kind: KubernetesKind, Kubernetes: &KubernetesTarget{Context: "staging", Namespace: "ops", Pod: "api-0", Container: "app"}}
	name, args, err := target.ExecCommand(TargetExecAction{Command: []string{"uname", "-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if name != "kubectl" {
		t.Fatalf("binary = %q", name)
	}
	want := []string{"--context", "staging", "exec", "-n", "ops", "api-0", "-c", "app", "--", "uname", "-a"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %+v, want %+v", args, want)
	}
	override := Target{Kind: KubernetesKind, Kubernetes: &KubernetesTarget{Namespace: "ops", Pod: "api-0", Container: "app"}}
	_, args, err = override.ExecCommand(TargetExecAction{Container: "sidecar", Command: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"exec", "-n", "ops", "api-0", "-c", "sidecar", "--", "true"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %+v, want %+v", args, want)
	}
	withoutContainer := Target{Kind: KubernetesKind, Kubernetes: &KubernetesTarget{Namespace: "ops", Pod: "api-0"}}
	_, args, err = withoutContainer.ExecCommand(TargetExecAction{Command: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"exec", "-n", "ops", "api-0", "--", "true"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %+v, want %+v", args, want)
	}
}

func TestTargetExecCommandRejectsUnsafeAndIncompleteTargets(t *testing.T) {
	dockerTarget := Target{Kind: DockerContextKind, Docker: &DockerContextTarget{Name: "prod"}}
	if _, _, err := dockerTarget.ExecCommand(TargetExecAction{Container: "web"}); !errors.Is(err, ErrEmptyAction) {
		t.Fatalf("empty command err = %v", err)
	}
	if _, _, err := dockerTarget.ExecCommand(TargetExecAction{Command: []string{"ps"}}); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("missing container err = %v", err)
	}
	flagContext := Target{Kind: DockerContextKind, Docker: &DockerContextTarget{Name: "-evil"}}
	if _, _, err := flagContext.ExecCommand(TargetExecAction{Container: "web", Command: []string{"true"}}); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("flag-prefixed context err = %v", err)
	}
	flagContainer := Target{Kind: DockerContextKind, Docker: &DockerContextTarget{Name: "prod"}}
	if _, _, err := flagContainer.ExecCommand(TargetExecAction{Container: "--privileged", Command: []string{"true"}}); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("flag-prefixed container err = %v", err)
	}
	contextOnly := Target{Kind: KubernetesKind, Kubernetes: &KubernetesTarget{Context: "staging"}}
	if _, _, err := contextOnly.ExecCommand(TargetExecAction{Command: []string{"true"}}); !errors.Is(err, ErrNotExecutable) {
		t.Fatalf("context-only err = %v", err)
	}
	for _, kubernetes := range []KubernetesTarget{
		{Context: "staging", Namespace: "-ops", Pod: "api-0"},
		{Context: "staging", Namespace: "ops", Pod: "-api-0"},
		{Context: "staging", Namespace: "ops", Pod: "api-0", Container: "-app"},
		{Context: "staging", Pod: "api-0"},
	} {
		target := Target{Kind: KubernetesKind, Kubernetes: &kubernetes}
		if _, _, err := target.ExecCommand(TargetExecAction{Command: []string{"true"}}); err == nil {
			t.Fatalf("unsafe kubernetes target was accepted: %+v", kubernetes)
		}
	}
	if _, _, err := (Target{}).ExecCommand(TargetExecAction{Command: []string{"true"}}); !errors.Is(err, ErrNotExecutable) {
		t.Fatalf("empty target err = %v", err)
	}
}

func TestRunTargetExecutesResolvedCommand(t *testing.T) {
	runner := &fakeRunner{outputs: map[string][]byte{
		"docker --context prod exec web sh -c uptime": []byte("up 1 day\n"),
	}}
	target := Target{Kind: DockerContextKind, Docker: &DockerContextTarget{Name: "prod"}}
	output, err := RunTarget(context.Background(), runner, target, TargetExecAction{Container: "web", Command: []string{"sh", "-c", "uptime"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "up 1 day\n" {
		t.Fatalf("output = %q", output)
	}
}

func TestRunTargetSurfacesCommandFailure(t *testing.T) {
	runner := &fakeRunner{errors: map[string]error{
		"kubectl exec -n ops api-0 -- true": errors.New("pod not found"),
	}}
	target := Target{Kind: KubernetesKind, Kubernetes: &KubernetesTarget{Namespace: "ops", Pod: "api-0"}}
	if _, err := RunTarget(context.Background(), runner, target, TargetExecAction{Command: []string{"true"}}); err == nil || !strings.Contains(err.Error(), "pod not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunTargetGatedRequiresKubeconfigAuthorization(t *testing.T) {
	podTarget := Target{Kind: KubernetesKind, Kubernetes: &KubernetesTarget{Namespace: "ops", Pod: "api-0"}}
	execAction := TargetExecAction{Command: []string{"true"}}
	runner := &fakeRunner{outputs: map[string][]byte{
		"kubectl exec -n ops api-0 -- true": []byte("ok"),
	}}
	if _, err := RunTargetGated(context.Background(), runner, nil, podTarget, execAction); !errors.Is(err, ErrKubeconfigNotAuthorized) {
		t.Fatalf("nil gate err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unauthorized exec ran commands: %+v", runner.calls)
	}
	deny := KubeconfigAuthorizerFunc(func(context.Context) error { return errors.New("not granted") })
	if _, err := RunTargetGated(context.Background(), runner, deny, podTarget, execAction); !errors.Is(err, ErrKubeconfigNotAuthorized) || !strings.Contains(err.Error(), "not granted") {
		t.Fatalf("deny gate err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("denied exec ran commands: %+v", runner.calls)
	}
	if _, err := RunTargetGated(context.Background(), runner, KubeconfigAllowed(true), podTarget, execAction); err != nil {
		t.Fatalf("allowed gate err = %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("allowed exec calls = %+v", runner.calls)
	}
	dockerTarget := Target{Kind: DockerContextKind, Docker: &DockerContextTarget{Name: "prod"}}
	runner.outputs["docker --context prod exec web true"] = []byte("[]\n")
	if _, err := RunTargetGated(context.Background(), runner, nil, dockerTarget, TargetExecAction{Container: "web", Command: []string{"true"}}); err != nil {
		t.Fatalf("docker target must not consult the kubeconfig gate: %v", err)
	}
}

func TestDiscoverKubernetesGated(t *testing.T) {
	runner := &fakeRunner{outputs: map[string][]byte{
		"kubectl config get-contexts -o json": []byte(`{"current-context":"staging","contexts":[{"name":"staging","context":{"cluster":"c"}}]}`),
		"kubectl get pods -A -o json":         []byte(`{"items":[]}`),
	}}
	if _, err := DiscoverKubernetesGated(context.Background(), runner, KubeconfigAllowed(false)); !errors.Is(err, ErrKubeconfigNotAuthorized) {
		t.Fatalf("denied discovery err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("denied discovery ran commands: %+v", runner.calls)
	}
	candidates, err := DiscoverKubernetesGated(context.Background(), runner, KubeconfigAllowed(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Name != "staging" {
		t.Fatalf("candidates = %+v", candidates)
	}
}
