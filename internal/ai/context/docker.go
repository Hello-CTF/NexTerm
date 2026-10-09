package aicontext

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/docker"
)

func WithDocker(deps Dependencies, service *docker.Service) Dependencies {
	deps.Containers = func(ctx context.Context, sessionID string) ([]tools.Container, error) {
		return tools.DockerContainers(ctx, service, sessionID)
	}
	return deps
}
