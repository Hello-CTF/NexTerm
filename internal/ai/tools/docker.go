package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var containerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func validContainer(value string) bool {
	return containerName.MatchString(value)
}

func (r *Registry) dockerPS(ctx context.Context, scope Scope) Output {
	if r.deps.DockerPS == nil {
		return Fail(ErrDockerUnavailable)
	}
	containers, err := r.deps.DockerPS(ctx, scope.SessionID)
	if err != nil {
		return Fail(err)
	}
	if len(containers) == 0 {
		return OK("没有容器")
	}
	var output strings.Builder
	for _, container := range containers {
		fmt.Fprintf(&output, "%s [%s] %s (%s) %s\n", container.Name, container.State, container.Image, container.Status, container.Ports)
	}
	text, truncated := capText(output.String())
	return Output{OK: true, Text: text, Truncated: truncated}
}

func (r *Registry) dockerLogs(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		Container string `json:"container_id"`
		Tail      int    `json:"tail"`
		Grep      string `json:"grep"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if !validContainer(args.Container) {
		return Fail(invalid("container_id 含非法字符"))
	}
	if args.Tail == 0 {
		args.Tail = 200
	}
	if args.Tail < 1 || args.Tail > 5000 {
		return Fail(invalid("tail 必须在 1-5000 之间"))
	}
	if r.deps.DockerLogs == nil {
		return Fail(ErrDockerUnavailable)
	}
	logs, err := r.deps.DockerLogs(ctx, scope.SessionID, args.Container, args.Tail, args.Grep)
	if err != nil {
		return Fail(err)
	}
	if logs == "" {
		logs = "日志为空"
	}
	text, truncated := capText(logs)
	return Output{OK: true, Text: text, Truncated: truncated}
}

func (r *Registry) dockerExec(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		Container string `json:"container_id"`
		Command   string `json:"cmd"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if !validContainer(args.Container) || strings.TrimSpace(args.Command) == "" {
		return Fail(invalid("container_id 或 cmd 无效"))
	}
	if r.deps.DockerExec == nil {
		return Fail(ErrDockerUnavailable)
	}
	result, err := r.deps.DockerExec(ctx, scope.SessionID, args.Container, args.Command)
	if err != nil {
		return Fail(err)
	}
	text := result.Output
	if text == "" {
		text = "(无输出)"
	}
	text, truncated := capText(text)
	return Output{OK: result.ExitCode == 0, Text: text, ExitCode: result.ExitCode, Truncated: truncated}
}

func (r *Registry) dockerControl(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		Container string `json:"container_id"`
		Action    string `json:"action"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if !validContainer(args.Container) {
		return Fail(invalid("container_id 含非法字符"))
	}
	switch args.Action {
	case "start", "stop", "restart", "rm":
	default:
		return Fail(invalid("不支持的 Docker action %s", args.Action))
	}
	if r.deps.DockerAct == nil {
		return Fail(ErrDockerUnavailable)
	}
	if err := r.deps.DockerAct(ctx, scope.SessionID, args.Container, args.Action); err != nil {
		return Fail(err)
	}
	if r.deps.Audit != nil && !r.deps.DockerActionAudited {
		_ = r.deps.Audit(context.WithoutCancel(ctx), AuditEntry{SessionID: scope.SessionID, AssetID: scope.AssetID, Kind: "docker_action", Payload: map[string]any{"container": args.Container, "action": args.Action}})
	}
	return OK(fmt.Sprintf("%s %s 完成", args.Action, args.Container))
}
