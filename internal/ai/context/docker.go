package aicontext

import (
	"context"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/docker"
)

func WithDocker(deps Dependencies, service *docker.Service) Dependencies {
	deps.Containers = func(ctx context.Context, sessionID string) ([]tools.Container, error) {
		return tools.DockerContainers(ctx, service, sessionID)
	}
	return deps
}
