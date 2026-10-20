package inventory

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Live Docker and Kubernetes verification is not available in unit tests;
// these checks only run when explicitly enabled and keep every action
// read-only except the opt-in pod exec.

func TestRealDockerContextTargets(t *testing.T) {
	if os.Getenv("NEXTERM_DOCKER_E2E") != "1" {
		t.Skip("set NEXTERM_DOCKER_E2E=1 to run live Docker context verification")
	}
	ctx := context.Background()
	candidates, err := DiscoverDockerContexts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	contextName := os.Getenv("NEXTERM_DOCKER_TEST_CONTEXT")
	if contextName == "" {
		contextName = "default"
	}
	var resolved *Target
	for _, candidate := range candidates {
		if candidate.Name != contextName {
			continue
		}
		target, err := ResolveTarget(candidate)
		if err != nil {
			t.Fatal(err)
		}
		resolved = &target
	}
	if resolved == nil {
		t.Fatalf("docker context %q not found in %+v", contextName, candidates)
	}
	if _, err := RunTarget(ctx, nil, *resolved, "ps"); err != nil {
		t.Fatalf("docker context %q is not actionable: %v", contextName, err)
	}
}

func TestRealKubernetesTargets(t *testing.T) {
	if os.Getenv("NEXTERM_K8S_E2E") != "1" {
		t.Skip("set NEXTERM_K8S_E2E=1 to run live Kubernetes verification")
	}
	ctx := context.Background()
	candidates, err := DiscoverKubernetesGated(ctx, nil, KubeconfigAllowed(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("discovered %d kubernetes candidates", len(candidates))
	podSpec := os.Getenv("NEXTERM_K8S_TEST_POD")
	if podSpec == "" {
		t.Log("NEXTERM_K8S_TEST_POD is empty; skipping pod exec verification")
		return
	}
	parts := strings.Split(podSpec, "/")
	if len(parts) != 3 {
		t.Fatalf("NEXTERM_K8S_TEST_POD must be namespace/pod/container, got %q", podSpec)
	}
	target := Target{Kind: KubernetesKind, Kubernetes: &KubernetesTarget{
		Namespace: parts[0], Pod: parts[1], Container: parts[2],
	}}
	output, err := RunTargetGated(ctx, nil, KubeconfigAllowed(true), target, "printf", "nexterm-k8s-e2e")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(output)) != "nexterm-k8s-e2e" {
		t.Fatalf("exec output = %q", output)
	}
}
