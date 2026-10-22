package inventory

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const inventoryTargetOptionKey = "inventoryTarget"

var (
	ErrNotExecutable           = errors.New("inventory target is not executable")
	ErrEmptyAction             = errors.New("target action is required")
	ErrInvalidTarget           = errors.New("invalid inventory target")
	ErrKubeconfigNotAuthorized = errors.New("server-local kubeconfig requires explicit authorization")
)

// DockerContextTarget addresses a Docker CLI context.
type DockerContextTarget struct {
	Name     string
	Endpoint string
}

// DockerEndpoint is the parsed address a Docker context points at.
type DockerEndpoint struct {
	Scheme   string // "ssh", "tcp", "unix", "npipe"
	Host     string
	Port     string
	Username string
}

// ParseDockerEndpoint parses a Docker context endpoint. Only ssh and tcp
// endpoints carry a remote host that can be authorized against a managed
// session; unix and npipe sockets stay host-less.
func ParseDockerEndpoint(raw string) (DockerEndpoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DockerEndpoint{}, errors.New("docker context endpoint is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return DockerEndpoint{}, fmt.Errorf("docker context endpoint: %w", err)
	}
	endpoint := DockerEndpoint{Scheme: strings.ToLower(parsed.Scheme)}
	switch endpoint.Scheme {
	case "ssh", "tcp":
		endpoint.Host = parsed.Hostname()
		endpoint.Port = parsed.Port()
		if endpoint.Scheme == "ssh" && parsed.User != nil {
			endpoint.Username = parsed.User.Username()
		}
		if endpoint.Host == "" {
			return DockerEndpoint{}, fmt.Errorf("docker context endpoint %q has no host", raw)
		}
	case "unix", "npipe":
	default:
		return DockerEndpoint{}, fmt.Errorf("docker context endpoint %q has unsupported scheme %q", raw, endpoint.Scheme)
	}
	return endpoint, nil
}

// KubernetesTarget addresses a pod inside a Kubernetes context. Pod is empty
// for context-level entries, which navigate but cannot exec.
type KubernetesTarget struct {
	Context   string
	Namespace string
	Pod       string
	Container string
}

// Target is a resolved actionable inventory target.
type Target struct {
	Kind       string
	Docker     *DockerContextTarget
	Kubernetes *KubernetesTarget
}

// KubeconfigAuthorizer decides whether the server-local kubeconfig may be
// used. Implementations must return nil to allow and an error to deny.
type KubeconfigAuthorizer interface {
	AuthorizeLocalKubeconfig(ctx context.Context) error
}

type KubeconfigAuthorizerFunc func(ctx context.Context) error

func (f KubeconfigAuthorizerFunc) AuthorizeLocalKubeconfig(ctx context.Context) error {
	return f(ctx)
}

// KubeconfigAllowed is a static KubeconfigAuthorizer; the zero value denies.
type KubeconfigAllowed bool

func (a KubeconfigAllowed) AuthorizeLocalKubeconfig(context.Context) error {
	if !a {
		return ErrKubeconfigNotAuthorized
	}
	return nil
}

func requireKubeconfig(ctx context.Context, gate KubeconfigAuthorizer) error {
	if gate == nil {
		return ErrKubeconfigNotAuthorized
	}
	if err := gate.AuthorizeLocalKubeconfig(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrKubeconfigNotAuthorized, err)
	}
	return nil
}

// ResolveTarget converts a discovery candidate into an actionable target.
func ResolveTarget(candidate Candidate) (Target, error) {
	raw, exists := candidate.Options[inventoryTargetOptionKey]
	if !exists {
		return Target{}, fmt.Errorf("%w: candidate %q has no inventory target", ErrInvalidTarget, candidate.Key)
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return Target{}, fmt.Errorf("%w: candidate %q has an invalid inventory target", ErrInvalidTarget, candidate.Key)
	}
	typeName, _ := fields["type"].(string)
	switch candidate.Kind {
	case DockerContextKind:
		if typeName != "context" {
			return Target{}, fmt.Errorf("%w: docker candidate %q has unsupported target type %q", ErrInvalidTarget, candidate.Key, typeName)
		}
		return Target{Kind: candidate.Kind, Docker: &DockerContextTarget{
			Name: targetString(fields, "name"), Endpoint: targetString(fields, "dockerEndpoint"),
		}}, nil
	case KubernetesKind:
		switch typeName {
		case "context":
			return Target{Kind: candidate.Kind, Kubernetes: &KubernetesTarget{Context: targetString(fields, "name")}}, nil
		case "pod":
			return Target{Kind: candidate.Kind, Kubernetes: &KubernetesTarget{
				Context:   targetString(fields, "context"),
				Namespace: targetString(fields, "namespace"),
				Pod:       targetString(fields, "pod"),
				Container: targetString(fields, "container"),
			}}, nil
		default:
			return Target{}, fmt.Errorf("%w: kubernetes candidate %q has unsupported target type %q", ErrInvalidTarget, candidate.Key, typeName)
		}
	default:
		return Target{}, fmt.Errorf("%w: candidate %q has unsupported target kind %q", ErrInvalidTarget, candidate.Key, candidate.Kind)
	}
}

func targetString(fields map[string]any, key string) string {
	value, _ := fields[key].(string)
	return strings.TrimSpace(value)
}

// TargetExecAction runs a command inside the target's container or pod. It
// is the only action the inventory target API exposes: free-form Docker or
// kubectl argv is deliberately not representable.
type TargetExecAction struct {
	// Container is the container name or ID. Required for Docker context
	// targets; optional override for Kubernetes pod targets.
	Container string
	// Command is the argv executed inside the container or pod.
	Command []string
}

// ExecCommand returns the binary and argv that run action on the target.
func (t Target) ExecCommand(action TargetExecAction) (string, []string, error) {
	if len(action.Command) == 0 {
		return "", nil, ErrEmptyAction
	}
	switch {
	case t.Docker != nil:
		if err := validateTargetValue("docker context", t.Docker.Name); err != nil {
			return "", nil, err
		}
		container := strings.TrimSpace(action.Container)
		if container == "" {
			return "", nil, fmt.Errorf("%w: docker context exec requires a container", ErrInvalidTarget)
		}
		if err := validateTargetValue("docker container", container); err != nil {
			return "", nil, err
		}
		args := make([]string, 0, len(action.Command)+4)
		args = append(args, "--context", t.Docker.Name, "exec", container)
		return "docker", append(args, action.Command...), nil
	case t.Kubernetes != nil:
		kubernetes := t.Kubernetes
		if kubernetes.Pod == "" {
			return "", nil, fmt.Errorf("%w: kubernetes context %q has no pod", ErrNotExecutable, kubernetes.Context)
		}
		for _, field := range []struct{ name, value string }{
			{"kubernetes namespace", kubernetes.Namespace},
			{"kubernetes pod", kubernetes.Pod},
		} {
			if field.value == "" {
				return "", nil, fmt.Errorf("%w: kubernetes target %s is required", ErrInvalidTarget, field.name)
			}
			if err := validateTargetValue(field.name, field.value); err != nil {
				return "", nil, err
			}
		}
		if kubernetes.Context != "" {
			if err := validateTargetValue("kubernetes context", kubernetes.Context); err != nil {
				return "", nil, err
			}
		}
		container := strings.TrimSpace(action.Container)
		if container == "" {
			container = kubernetes.Container
		}
		if container != "" {
			if err := validateTargetValue("kubernetes container", container); err != nil {
				return "", nil, err
			}
		}
		args := make([]string, 0, len(action.Command)+10)
		if kubernetes.Context != "" {
			args = append(args, "--context", kubernetes.Context)
		}
		args = append(args, "exec", "-n", kubernetes.Namespace, kubernetes.Pod)
		if container != "" {
			args = append(args, "-c", container)
		}
		args = append(args, "--")
		return "kubectl", append(args, action.Command...), nil
	default:
		return "", nil, ErrNotExecutable
	}
}

func validateTargetValue(name, value string) error {
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("%w: %s %q must not start with a flag prefix", ErrInvalidTarget, name, value)
	}
	return nil
}

// RunTarget executes action on the target through runner.
func RunTarget(ctx context.Context, runner CommandRunner, target Target, action TargetExecAction) ([]byte, error) {
	if runner == nil {
		runner = ExecRunner{}
	}
	name, args, err := target.ExecCommand(action)
	if err != nil {
		return nil, err
	}
	output, err := runner.Output(ctx, name, args...)
	if err != nil {
		return nil, fmt.Errorf("%s target action failed: %w", target.Kind, err)
	}
	return output, nil
}

// RunTargetGated executes action on the target after requiring explicit
// authorization whenever the target reads the server-local kubeconfig.
func RunTargetGated(ctx context.Context, runner CommandRunner, gate KubeconfigAuthorizer, target Target, action TargetExecAction) ([]byte, error) {
	if target.Kubernetes != nil {
		if err := requireKubeconfig(ctx, gate); err != nil {
			return nil, err
		}
	}
	return RunTarget(ctx, runner, target, action)
}

// DiscoverKubernetesGated runs Kubernetes discovery only after the gate
// explicitly authorizes use of the server-local kubeconfig.
func DiscoverKubernetesGated(ctx context.Context, runner CommandRunner, gate KubeconfigAuthorizer) ([]Candidate, error) {
	if err := requireKubeconfig(ctx, gate); err != nil {
		return nil, err
	}
	return DiscoverKubernetes(ctx, runner)
}
