package docker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/moby/moby/client"
)

func TestRealDockerMaintenanceReadOnly(t *testing.T) {
	if os.Getenv("NEXTERM_DOCKER_E2E") != "1" {
		t.Skip("set NEXTERM_DOCKER_E2E=1 to run read-only Docker maintenance integration tests")
	}
	cli, err := NewClient(ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if _, err := cli.Ping(t.Context(), client.PingOptions{}); err != nil {
		t.Fatal(err)
	}
	command := NewCommandBackend(CommandRunnerFunc(runLocalDockerTestCommand), nil, ShellPOSIX)
	service := NewService(StaticBackends{SDKBackend: NewMobyBackend(cli), CommandBackend: command})
	probe, err := service.Reclaimable(t.Context(), "local")
	if err != nil {
		t.Fatalf("reclaimable probe: %v", err)
	}
	if len(probe.Categories) != 4 || probe.TotalBytes < 0 {
		t.Fatalf("invalid reclaimable probe: %+v", probe)
	}
	usage, err := service.DiskUsage(t.Context(), "local")
	if err != nil {
		t.Fatalf("disk usage: %v", err)
	}
	if len(usage.Categories) != 4 || len(usage.Networks) == 0 {
		t.Fatalf("incomplete disk usage: categories=%d networks=%d", len(usage.Categories), len(usage.Networks))
	}
	usedVolumes := 0
	for _, volume := range usage.Volumes {
		if volume.Links > 0 && len(volume.Containers) > 0 {
			usedVolumes++
		}
		for _, container := range volume.Containers {
			if container.ID == "" || container.Name == "" {
				t.Errorf("incomplete volume container relation: %+v", container)
			}
		}
	}
	usedNetworks := 0
	for _, network := range usage.Networks {
		if len(network.Containers) > 0 {
			usedNetworks++
		}
		for _, container := range network.Containers {
			if container.ID == "" || container.Name == "" {
				t.Errorf("incomplete network container relation: %+v", container)
			}
		}
	}
	if usedVolumes == 0 || usedNetworks == 0 {
		t.Logf("conditional relation smoke: usedVolumes=%d usedNetworks=%d", usedVolumes, usedNetworks)
	}
	compose, err := service.ComposeStacks(t.Context(), "local")
	if err != nil {
		t.Fatalf("compose stacks: %v", err)
	}
	if !compose.Available || (compose.CLI != ComposeCLIPlugin && compose.CLI != ComposeCLILegacy) {
		t.Fatalf("compose CLI unavailable: %+v", compose)
	}
	preview, err := service.PreviewPrune(t.Context(), "local", []PruneKind{
		PruneKindContainers, PruneKindImages, PruneKindVolumes, PruneKindNetworks, PruneKindBuildCache,
	})
	if err != nil {
		t.Fatalf("prune preview: %v", err)
	}
	if preview.Token == "" || preview.Digest == "" {
		t.Fatalf("incomplete preview: %+v", preview)
	}
	var total int64
	for _, item := range preview.Items {
		if item.ID == "" || item.Bytes < 0 {
			t.Errorf("invalid preview item: %+v", item)
		}
		total += item.Bytes
	}
	if total != preview.TotalBytes {
		t.Fatalf("preview bytes = %d, want %d", total, preview.TotalBytes)
	}
}

func runLocalDockerTestCommand(ctx context.Context, command string) (CommandResult, error) {
	process := exec.CommandContext(ctx, "sh", "-c", command)
	var stdout, stderr bytes.Buffer
	process.Stdout = &stdout
	process.Stderr = &stderr
	err := process.Run()
	result := CommandResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		result.ExitCode = &code
		return result, nil
	}
	return result, err
}
