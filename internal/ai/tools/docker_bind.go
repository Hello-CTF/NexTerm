package tools

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/docker"
)

func DockerContainers(ctx context.Context, service *docker.Service, sessionID string) ([]Container, error) {
	containers, err := service.PS(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	result := make([]Container, len(containers))
	for i, container := range containers {
		result[i] = Container{ID: container.ID, Name: container.Name, Image: container.Image, State: container.State, Status: container.Status, Ports: container.Ports}
	}
	return result, nil
}

func WithDocker(deps Dependencies, service *docker.Service, assetID string) Dependencies {
	deps.DockerPS = func(ctx context.Context, sessionID string) ([]Container, error) {
		return DockerContainers(ctx, service, sessionID)
	}
	deps.DockerLogs = func(ctx context.Context, sessionID, container string, tail int, grep string) (string, error) {
		return service.Logs(ctx, docker.LogsRequest{SessionID: sessionID, Container: container, Tail: tail, Grep: grep})
	}
	deps.DockerExec = func(ctx context.Context, sessionID, container, command string) (ExecResult, error) {
		result, err := service.Exec(ctx, docker.ExecRequest{SessionID: sessionID, Container: container, Command: command})
		return ExecResult{Output: result.Output, ExitCode: result.ExitCode}, err
	}
	deps.DockerAct = func(ctx context.Context, sessionID, container, action string) error {
		mapped := docker.ActionStart
		switch action {
		case "stop":
			mapped = docker.ActionStop
		case "restart":
			mapped = docker.ActionRestart
		case "rm":
			mapped = docker.ActionRemove
		}
		return service.Action(ctx, docker.ActionRequest{SessionID: sessionID, AssetID: assetID, Source: "ai", Options: docker.ActionOptions{Container: container, Action: mapped}})
	}
	deps.DockerActionAudited = true
	return deps
}
