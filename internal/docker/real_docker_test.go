package docker

import (
	"os"
	"testing"

	"github.com/moby/moby/client"
)

func TestRealDockerDesktop(t *testing.T) {
	if os.Getenv("NEXTERM_DOCKER_E2E") != "1" {
		t.Skip("set NEXTERM_DOCKER_E2E=1 to run read-only Docker Desktop integration tests")
	}
	cli, err := NewClient(ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if _, err := cli.Ping(t.Context(), client.PingOptions{}); err != nil {
		t.Fatal(err)
	}
	backend := NewMobyBackend(cli)
	service := NewService(StaticBackends{SDKBackend: backend})
	if _, err := service.PS(t.Context(), "local"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Images(t.Context(), "local"); err != nil {
		t.Fatal(err)
	}
	containerID := os.Getenv("NEXTERM_DOCKER_TEST_CONTAINER")
	if containerID == "" {
		t.Log("NEXTERM_DOCKER_TEST_CONTAINER is empty; skipping container-specific inspect/logs/stats/files/exec")
		return
	}
	if _, err := service.Inspect(t.Context(), "local", containerID); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.ContainerStats(t.Context(), containerID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Logs(t.Context(), LogsRequest{SessionID: "local", Container: containerID, Tail: 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ContainerListDir(t.Context(), "local", containerID, "/"); err != nil {
		t.Fatal(err)
	}
	result, err := service.Exec(t.Context(), ExecRequest{SessionID: "local", Container: containerID, Command: "printf nexterm-docker-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "nexterm-docker-e2e" || result.ExitCode != 0 {
		t.Fatalf("exec result = %+v", result)
	}
}
