package docker

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

func (b *CommandBackend) SystemDF(ctx context.Context) (string, error) {
	return b.run(ctx, []string{"system", "df"})
}

func (b *CommandBackend) DiskUsage(ctx context.Context) (DiskUsage, error) {
	summary, err := b.SystemDF(ctx)
	if err != nil {
		return DiskUsage{}, err
	}
	verbose, err := b.run(ctx, []string{"system", "df", "-v"})
	if err != nil {
		return DiskUsage{}, err
	}
	usage, err := parseDockerDF(summary + "\n" + verbose)
	if err != nil {
		return DiskUsage{}, err
	}
	containerOutput, err := b.run(ctx, []string{"ps", "-a", "--format", "{{json .}}"})
	if err != nil {
		return DiskUsage{}, err
	}
	containers, err := decodeJSONLines[cliContainer](containerOutput)
	if err != nil {
		return DiskUsage{}, err
	}
	volumeOutput, err := b.run(ctx, []string{"volume", "ls", "--format", "{{json .}}"})
	if err != nil {
		return DiskUsage{}, err
	}
	volumeRows, err := decodeJSONLines[cliVolume](volumeOutput)
	if err != nil {
		return DiskUsage{}, err
	}
	for _, row := range volumeRows {
		found := false
		for index := range usage.Volumes {
			if usage.Volumes[index].Name == row.Name {
				usage.Volumes[index].Driver = row.Driver
				found = true
				break
			}
		}
		if !found {
			usage.Volumes = append(usage.Volumes, VolumeUsage{Name: row.Name, Driver: row.Driver, Containers: []ContainerUse{}})
		}
	}
	networkOutput, err := b.run(ctx, []string{"network", "ls", "--format", "{{json .}}"})
	if err != nil {
		return DiskUsage{}, err
	}
	networkRows, err := decodeJSONLines[cliNetwork](networkOutput)
	if err != nil {
		return DiskUsage{}, err
	}
	usage.Networks = make([]NetworkUsage, 0, len(networkRows))
	for _, row := range networkRows {
		usage.Networks = append(usage.Networks, NetworkUsage{
			ID: row.ID, Name: row.Name, Driver: row.Driver, Scope: row.Scope,
			Builtin: row.Name == "bridge" || row.Name == "host" || row.Name == "none", Containers: []ContainerUse{},
		})
	}
	for _, container := range containers {
		use := ContainerUse{ID: container.ID, Name: container.Names}
		mounts := splitDockerNames(container.Mounts)
		for volumeIndex := range usage.Volumes {
			if slicesContains(mounts, usage.Volumes[volumeIndex].Name) {
				usage.Volumes[volumeIndex].Containers = append(usage.Volumes[volumeIndex].Containers, use)
			}
		}
		networks := splitDockerNames(container.Networks)
		for networkIndex := range usage.Networks {
			if slicesContains(networks, usage.Networks[networkIndex].Name) {
				usage.Networks[networkIndex].Containers = append(usage.Networks[networkIndex].Containers, use)
			}
		}
	}
	sort.Slice(usage.Volumes, func(i, j int) bool { return usage.Volumes[i].Name < usage.Volumes[j].Name })
	sort.Slice(usage.Networks, func(i, j int) bool { return usage.Networks[i].Name < usage.Networks[j].Name })
	return usage, nil
}

func splitDockerNames(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (b *CommandBackend) ReclaimableBuildCache(ctx context.Context) ([]PruneItem, error) {
	output, err := b.run(ctx, []string{"buildx", "du", "--format", "{{json .}}"})
	if err != nil {
		return nil, fmt.Errorf("read reclaimable build cache: %w", err)
	}
	rows, err := decodeJSONLines[cliBuildCache](output)
	if err != nil {
		return nil, err
	}
	items := make([]PruneItem, 0)
	for _, row := range rows {
		if !row.Reclaimable || row.ID == "" {
			continue
		}
		name := row.Description
		if name == "" {
			name = row.ID
		}
		items = append(items, PruneItem{Kind: PruneKindBuildCache, ID: row.ID, Name: name, Bytes: int64(row.Size)})
	}
	return items, nil
}

func (b *CommandBackend) RemovePruneItem(ctx context.Context, item PruneItem) (string, error) {
	var args []string
	switch item.Kind {
	case PruneKindContainers:
		args = []string{"container", "rm", item.ID}
	case PruneKindImages:
		args = []string{"image", "rm", item.ID}
	case PruneKindVolumes:
		args = []string{"volume", "rm", item.ID}
	case PruneKindNetworks:
		args = []string{"network", "rm", item.ID}
	case PruneKindBuildCache:
		args = []string{"buildx", "prune", "--force", "--filter", "id=" + item.ID}
	default:
		return "", fmt.Errorf("%w: prune kind %s", ErrInvalidAction, item.Kind)
	}
	result, err := b.runResult(ctx, args)
	return result.Stdout + result.Stderr, err
}

type cliVolume struct {
	Name   string `json:"Name"`
	Driver string `json:"Driver"`
}

type cliNetwork struct {
	ID     string `json:"ID"`
	Name   string `json:"Name"`
	Driver string `json:"Driver"`
	Scope  string `json:"Scope"`
}

type cliBuildCache struct {
	ID          string           `json:"ID"`
	Description string           `json:"Description"`
	Reclaimable bool             `json:"Reclaimable"`
	Size        dockerBytesValue `json:"Size"`
}
