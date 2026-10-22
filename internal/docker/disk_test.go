package docker

import (
	"fmt"
	"strings"
	"testing"
)

func dfTestTable(columns []string, rows ...[]string) string {
	widths := make([]int, len(columns))
	for index, column := range columns {
		widths[index] = len(column)
	}
	for _, row := range rows {
		for index, value := range row {
			widths[index] = max(widths[index], len(value))
		}
	}
	formatRow := func(row []string) string {
		padded := make([]string, len(row))
		for index, value := range row {
			padded[index] = fmt.Sprintf("%-*s", widths[index], value)
		}
		return strings.Join(padded, "  ") + "\n"
	}
	var output strings.Builder
	output.WriteString(formatRow(columns))
	for _, row := range rows {
		output.WriteString(formatRow(row))
	}
	return output.String()
}

func testDFOutput() string {
	var output strings.Builder
	output.WriteString("TYPE                TOTAL  ACTIVE  SIZE     RECLAIMABLE\n")
	output.WriteString("Images              2      1       1GB      300MB (30%)\n")
	output.WriteString("Containers          2      1       4kB      2kB (50%)\n")
	output.WriteString("Local Volumes       2      1       11MB     1MB (9%)\n")
	output.WriteString("Build Cache         2      0       8kB      4kB (50%)\n\n")
	output.WriteString("Images space usage:\n")
	output.WriteString(dfTestTable(
		[]string{"REPOSITORY", "TAG", "IMAGE ID", "CREATED", "SIZE", "SHARED SIZE", "UNIQUE SIZE", "CONTAINERS"},
		[]string{"<none>", "<none>", "dangling1", "2 days ago", "300MB", "0B", "300MB", "0"},
		[]string{"nginx", "latest", "used1", "3 days ago", "1GB", "100MB", "900MB", "1"},
	))
	output.WriteString("\nContainers space usage:\n")
	output.WriteString(dfTestTable(
		[]string{"CONTAINER ID", "IMAGE", "COMMAND", "LOCAL VOLUMES", "SIZE", "CREATED", "STATUS", "NAMES"},
		[]string{"running1", "nginx:latest", "\"nginx -g\"", "1", "2kB", "2 hours ago", "Up 2 hours", "web"},
		[]string{"exited1", "nginx:latest", "\"nginx -g\"", "0", "2kB", "3 hours ago", "Exited (0) 3 hours ago", "old-web"},
	))
	output.WriteString("\nLocal Volumes space usage:\n")
	output.WriteString(dfTestTable(
		[]string{"VOLUME NAME", "LINKS", "SIZE"},
		[]string{"used-volume", "1", "10MB"},
		[]string{"orphan-volume", "0", "1MB"},
	))
	output.WriteString("\nBuild cache usage:\n")
	output.WriteString(dfTestTable(
		[]string{"CACHE ID", "CACHE TYPE", "SIZE", "CREATED", "LAST USED", "USAGE", "SHARED"},
		[]string{"cache1", "regular", "4kB", "2 days ago", "1 day ago", "1", "false"},
	))
	return output.String()
}

func TestParseDockerDFSummaryAndDetails(t *testing.T) {
	usage, err := parseDockerDF(testDFOutput())
	if err != nil {
		t.Fatal(err)
	}
	if len(usage.Categories) != 4 || usage.Categories[0].ReclaimableBytes != 300_000_000 || usage.Categories[3].ReclaimableBytes != 4000 {
		t.Fatalf("unexpected categories: %+v", usage.Categories)
	}
	if len(usage.Images) != 2 || usage.Images[0].UniqueSizeBytes != 300_000_000 || usage.Images[1].Containers != 1 {
		t.Fatalf("unexpected images: %+v", usage.Images)
	}
	if len(usage.Containers) != 2 || usage.Containers[1].Name != "old-web" || usage.Containers[1].Status != "Exited (0) 3 hours ago" || usage.Containers[1].SizeBytes != 2000 {
		t.Fatalf("unexpected containers: %+v", usage.Containers)
	}
	if len(usage.Volumes) != 2 || usage.Volumes[1].Links != 0 || usage.Volumes[1].SizeBytes != 1_000_000 {
		t.Fatalf("unexpected volumes: %+v", usage.Volumes)
	}
	if len(usage.BuildCache) != 1 || usage.BuildCache[0].SizeBytes != 4000 {
		t.Fatalf("unexpected build cache: %+v", usage.BuildCache)
	}
}

func TestDiskUsageRelationsPrunePreviewAndConfirmation(t *testing.T) {
	cli := &fakeDockerCLI{handler: func(command string) CommandResult {
		switch command {
		case "docker 'system' 'df'", "docker 'system' 'df' '-v'":
			return CommandResult{Stdout: testDFOutput()}
		case "docker 'ps' '-a' '--format' '{{json .}}'":
			return CommandResult{Stdout: "{\"ID\":\"running1\",\"Names\":\"web\",\"Mounts\":\"used-volume,/srv/bind\",\"Networks\":\"bridge,app-net\"}\n{\"ID\":\"exited1\",\"Names\":\"old-web\",\"Mounts\":\"\",\"Networks\":\"app-net\"}\n"}
		case "docker 'volume' 'ls' '--format' '{{json .}}'":
			return CommandResult{Stdout: "{\"Name\":\"used-volume\",\"Driver\":\"local\"}\n{\"Name\":\"orphan-volume\",\"Driver\":\"local\"}\n"}
		case "docker 'network' 'ls' '--format' '{{json .}}'":
			return CommandResult{Stdout: "{\"ID\":\"n1\",\"Name\":\"bridge\",\"Driver\":\"bridge\",\"Scope\":\"local\"}\n{\"ID\":\"n2\",\"Name\":\"app-net\",\"Driver\":\"bridge\",\"Scope\":\"local\"}\n{\"ID\":\"n3\",\"Name\":\"unused-net\",\"Driver\":\"bridge\",\"Scope\":\"local\"}\n"}
		case "docker 'buildx' 'du' '--format' '{{json .}}'":
			return CommandResult{Stdout: "{\"ID\":\"cache1\",\"Description\":\"cached layer\",\"Reclaimable\":true,\"Size\":\"4.096kB\"}\n{\"ID\":\"cache2\",\"Reclaimable\":false,\"Size\":9000}\n"}
		default:
			return CommandResult{Stdout: "removed\n"}
		}
	}}
	service := NewService(&stubProvider{sdkErr: ErrNoBackend, command: NewCommandBackend(cli, nil, ShellPOSIX)})
	usage, err := service.DiskUsage(t.Context(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(usage.Volumes[0].Containers) != 0 || len(usage.Volumes[1].Containers) != 1 || usage.Volumes[1].Containers[0].Name != "web" {
		t.Fatalf("volume relations = %+v", usage.Volumes)
	}
	if len(usage.Networks) != 3 || len(usage.Networks[0].Containers) != 2 || len(usage.Networks[1].Containers) != 1 || len(usage.Networks[2].Containers) != 0 {
		t.Fatalf("network relations = %+v", usage.Networks)
	}
	preview, err := service.PreviewPrune(t.Context(), "s1", []PruneKind{PruneKindContainers, PruneKindImages, PruneKindVolumes, PruneKindNetworks, PruneKindBuildCache})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Items) != 5 || preview.TotalBytes != 301_006_096 {
		t.Fatalf("preview = %+v", preview)
	}
	for _, item := range preview.Items {
		if item.ID == "running1" || item.ID == "used1" || item.ID == "used-volume" || item.ID == "app-net" || item.ID == "bridge" || item.ID == "cache2" {
			t.Fatalf("active resource included in prune preview: %+v", item)
		}
	}
	before := len(cli.all())
	if _, err := service.Prune(t.Context(), PruneRequest{SessionID: "s1", Token: preview.Token, Digest: "wrong"}); err == nil {
		t.Fatal("wrong preview digest was accepted")
	}
	if len(cli.all()) != before {
		t.Fatal("unconfirmed prune executed commands")
	}
	result, err := service.Prune(t.Context(), PruneRequest{SessionID: "s1", Token: preview.Token, Digest: preview.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 5 || result.FreedBytes != preview.TotalBytes {
		t.Fatalf("prune result = %+v", result)
	}
	commands := cli.all()
	for _, want := range []string{
		"docker 'buildx' 'prune' '--force' '--filter' 'id=cache1'",
		"docker 'container' 'rm' 'exited1'",
		"docker 'image' 'rm' 'dangling1'",
		"docker 'network' 'rm' 'unused-net'",
		"docker 'volume' 'rm' 'orphan-volume'",
	} {
		found := false
		for _, command := range commands {
			found = found || command == want
		}
		if !found {
			t.Errorf("missing command %q in %q", want, commands)
		}
	}
	if _, err := service.Prune(t.Context(), PruneRequest{SessionID: "s1", Token: preview.Token, Digest: preview.Digest}); err == nil {
		t.Fatal("used preview token was accepted twice")
	}
}

func TestReclaimableProbeUsesFixedReadOnlyCommand(t *testing.T) {
	cli := &fakeDockerCLI{results: map[string]CommandResult{"docker 'system' 'df'": {Stdout: testDFOutput()}}}
	service := NewService(&stubProvider{sdkErr: ErrNoBackend, command: NewCommandBackend(cli, nil, ShellPOSIX)})
	probe, err := service.Reclaimable(t.Context(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if probe.TotalBytes != 301_006_000 || len(probe.Categories) != 4 {
		t.Fatalf("probe = %+v", probe)
	}
	commands := cli.all()
	if len(commands) != 1 || commands[0] != "docker 'system' 'df'" {
		t.Fatalf("probe commands = %q", commands)
	}
}

func TestPruneCommandQuotesBuildCacheID(t *testing.T) {
	cli := &fakeDockerCLI{}
	backend := NewCommandBackend(cli, nil, ShellPOSIX)
	id := "cache';printf injected;#"
	if _, err := backend.RemovePruneItem(t.Context(), PruneItem{Kind: PruneKindBuildCache, ID: id}); err != nil {
		t.Fatal(err)
	}
	want := "docker 'buildx' 'prune' '--force' '--filter' " + quoteShell("id="+id, ShellPOSIX)
	if got := cli.all()[0]; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}
