package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/moby/moby/client"
)

func TestRealDockerAcceptance(t *testing.T) {
	if os.Getenv("NEXTERM_AI_ACCEPTANCE_DOCKER") != "1" {
		t.Skip("set NEXTERM_AI_ACCEPTANCE_DOCKER=1 to run read-only Docker acceptance")
	}
	cli, err := docker.NewClient(docker.ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if _, err := cli.Ping(context.Background(), client.PingOptions{}); err != nil {
		t.Fatal(err)
	}
	backend := docker.NewMobyBackend(cli)
	service := docker.NewService(docker.StaticBackends{SDKBackend: backend})
	defer service.Close()
	registry := NewRegistry(WithDocker(Dependencies{}, service, ""))
	result := registry.Execute(context.Background(), "acceptance", Scope{SessionID: "local"}, Call{ID: "ps", Name: "docker_ps", Args: json.RawMessage(`{}`)}, nil)
	if !result.OK {
		t.Fatalf("docker_ps: %+v", result)
	}
	containerID := os.Getenv("NEXTERM_DOCKER_TEST_CONTAINER")
	if containerID == "" {
		t.Log("container-specific logs/exec not requested")
		return
	}
	result = registry.Execute(context.Background(), "acceptance", Scope{SessionID: "local"}, Call{ID: "logs", Name: "docker_logs", Args: json.RawMessage(`{"container_id":` + strconvQuote(containerID) + `,"tail":20}`)}, nil)
	if !result.OK {
		t.Fatalf("docker_logs: %+v", result)
	}
	result = registry.Execute(context.Background(), "acceptance", Scope{SessionID: "local"}, Call{ID: "exec", Name: "docker_exec", Args: json.RawMessage(`{"container_id":` + strconvQuote(containerID) + `,"cmd":"printf nexterm-ai-acceptance"}`)}, nil)
	if !result.OK || !strings.Contains(result.Text, "nexterm-ai-acceptance") {
		t.Fatalf("docker_exec: %+v", result)
	}
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
