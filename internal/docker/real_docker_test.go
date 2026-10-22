package docker

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

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

func TestRealDockerExecCancelTerminatesStream(t *testing.T) {
	if os.Getenv("NEXTERM_DOCKER_E2E") != "1" {
		t.Skip("set NEXTERM_DOCKER_E2E=1 to run read-only Docker Desktop integration tests")
	}
	containerID := os.Getenv("NEXTERM_DOCKER_TEST_CONTAINER")
	if containerID == "" {
		t.Skip("NEXTERM_DOCKER_TEST_CONTAINER is empty; skipping exec cancel integration test")
	}
	cli, err := NewClient(ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	service := NewService(StaticBackends{SDKBackend: NewMobyBackend(cli)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := service.Exec(ctx, ExecRequest{SessionID: "local", Container: containerID, Command: "sleep 300"})
		done <- err
	}()
	// Let the exec reach the blocking read on the hijacked stream, then
	// cancel: the SDK stream must terminate and the exec must return.
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("exec error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled exec did not return; the SDK stream outlived its context")
	}
}
