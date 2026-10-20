package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

const (
	DockerContextKind = "docker-context"
	KubernetesKind    = "kubernetes"
	maxCandidates     = 10000
	maxOutputBytes    = 8 << 20
	maxErrorBytes     = 64 << 10
)

type Candidate struct {
	Key      string         `json:"key"`
	Kind     string         `json:"kind"`
	Name     string         `json:"name"`
	Host     *string        `json:"host"`
	Port     *int32         `json:"port"`
	Username *string        `json:"username"`
	AuthKind *string        `json:"authKind"`
	KeyPath  *string        `json:"keyPath"`
	CredID   *string        `json:"credId"`
	Options  map[string]any `json:"options"`
}

type CommandRunner interface {
	Output(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	if original > remaining {
		b.overflow = true
	}
	return original, nil
}

func (ExecRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	stdout := &boundedBuffer{limit: maxOutputBytes}
	stderr := &boundedBuffer{limit: maxErrorBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if stdout.overflow {
		return nil, fmt.Errorf("%s output exceeds %d bytes", name, maxOutputBytes)
	}
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return nil, fmt.Errorf("%s: %w: %s", name, err, message)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return stdout.Bytes(), nil
}

type dockerContext struct {
	Name               string `json:"Name"`
	Description        string `json:"Description"`
	DockerEndpoint     string `json:"DockerEndpoint"`
	KubernetesEndpoint string `json:"KubernetesEndpoint"`
	ContextType        string `json:"ContextType"`
	Current            bool   `json:"Current"`
}

func ParseDockerContexts(data []byte) ([]Candidate, error) {
	lines := bytes.Split(data, []byte("\n"))
	candidates := make([]Candidate, 0, len(lines))
	seen := map[string]bool{}
	for index, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var item dockerContext
		if err := json.Unmarshal(line, &item); err != nil {
			return nil, fmt.Errorf("docker context line %d: %w", index+1, err)
		}
		item.Name = strings.TrimSpace(item.Name)
		if item.Name == "" {
			return nil, fmt.Errorf("docker context line %d has no name", index+1)
		}
		key := "context/" + item.Name
		if seen[key] {
			return nil, fmt.Errorf("docker context %q is duplicated", item.Name)
		}
		seen[key] = true
		target := map[string]any{
			"type": "context", "name": item.Name, "description": strings.TrimSpace(item.Description),
			"dockerEndpoint": strings.TrimSpace(item.DockerEndpoint), "kubernetesEndpoint": strings.TrimSpace(item.KubernetesEndpoint),
			"contextType": strings.TrimSpace(item.ContextType), "current": item.Current,
		}
		var host *string
		if endpoint := strings.TrimSpace(item.DockerEndpoint); endpoint != "" {
			host = &endpoint
		}
		candidates = append(candidates, Candidate{
			Key: key, Kind: DockerContextKind, Name: item.Name, Host: host,
			Options: map[string]any{"inventoryTarget": target},
		})
		if len(candidates) > maxCandidates {
			return nil, fmt.Errorf("docker context count exceeds %d", maxCandidates)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })
	return candidates, nil
}

func DiscoverDockerContexts(ctx context.Context, runner CommandRunner) ([]Candidate, error) {
	if runner == nil {
		runner = ExecRunner{}
	}
	output, err := runner.Output(ctx, "docker", "context", "ls", "--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("docker context discovery failed: %w", err)
	}
	return ParseDockerContexts(output)
}

type kubernetesContextDocument struct {
	Contexts []struct {
		Name    string `json:"name"`
		Context struct {
			Cluster   string `json:"cluster"`
			User      string `json:"user"`
			Namespace string `json:"namespace"`
		} `json:"context"`
	} `json:"contexts"`
	Current string `json:"current-context"`
}

func ParseKubernetesContexts(data []byte) ([]Candidate, string, error) {
	var document kubernetesContextDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, "", fmt.Errorf("kubectl contexts: %w", err)
	}
	candidates := make([]Candidate, 0, len(document.Contexts))
	seen := map[string]bool{}
	for _, item := range document.Contexts {
		item.Name = strings.TrimSpace(item.Name)
		if item.Name == "" {
			return nil, "", errors.New("kubectl context has no name")
		}
		key := "context/" + item.Name
		if seen[key] {
			return nil, "", fmt.Errorf("kubectl context %q is duplicated", item.Name)
		}
		seen[key] = true
		candidates = append(candidates, Candidate{
			Key: key, Kind: KubernetesKind, Name: item.Name,
			Options: map[string]any{"inventoryTarget": map[string]any{
				"type": "context", "name": item.Name, "cluster": item.Context.Cluster,
				"user": item.Context.User, "namespace": item.Context.Namespace,
				"current": item.Name == document.Current,
			}},
		})
		if len(candidates) > maxCandidates {
			return nil, "", fmt.Errorf("kubernetes context count exceeds %d", maxCandidates)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })
	return candidates, strings.TrimSpace(document.Current), nil
}

type kubernetesPodDocument struct {
	Items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Spec struct {
			NodeName   string `json:"nodeName"`
			Containers []struct {
				Name string `json:"name"`
			} `json:"containers"`
		} `json:"spec"`
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	} `json:"items"`
}

func ParseKubernetesPods(data []byte, currentContext string) ([]Candidate, error) {
	var document kubernetesPodDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("kubectl pods: %w", err)
	}
	candidates := make([]Candidate, 0, len(document.Items))
	seen := map[string]bool{}
	for _, item := range document.Items {
		name := strings.TrimSpace(item.Metadata.Name)
		namespace := strings.TrimSpace(item.Metadata.Namespace)
		if name == "" || namespace == "" {
			return nil, errors.New("kubectl pod has no namespace or name")
		}
		key := "pod/" + currentContext + "/" + namespace + "/" + name
		if seen[key] {
			return nil, fmt.Errorf("kubernetes pod %s/%s is duplicated", namespace, name)
		}
		seen[key] = true
		containers := make([]string, 0, len(item.Spec.Containers))
		for _, container := range item.Spec.Containers {
			if containerName := strings.TrimSpace(container.Name); containerName != "" {
				containers = append(containers, containerName)
			}
		}
		container := ""
		if len(containers) > 0 {
			container = containers[0]
		}
		candidates = append(candidates, Candidate{
			Key: key, Kind: KubernetesKind, Name: namespace + "/" + name,
			Options: map[string]any{"inventoryTarget": map[string]any{
				"type": "pod", "context": currentContext, "namespace": namespace, "pod": name,
				"node": item.Spec.NodeName, "phase": item.Status.Phase,
				"container": container, "containers": containers,
			}},
		})
		if len(candidates) > maxCandidates {
			return nil, fmt.Errorf("kubernetes pod count exceeds %d", maxCandidates)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })
	return candidates, nil
}

func DiscoverKubernetes(ctx context.Context, runner CommandRunner) ([]Candidate, error) {
	if runner == nil {
		runner = ExecRunner{}
	}
	contextOutput, err := runner.Output(ctx, "kubectl", "config", "get-contexts", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("kubernetes context discovery failed: %w", err)
	}
	contexts, current, err := ParseKubernetesContexts(contextOutput)
	if err != nil {
		return nil, err
	}
	podOutput, err := runner.Output(ctx, "kubectl", "get", "pods", "-A", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("kubernetes pod discovery failed: %w", err)
	}
	pods, err := ParseKubernetesPods(podOutput, current)
	if err != nil {
		return nil, err
	}
	if len(contexts)+len(pods) > maxCandidates {
		return nil, fmt.Errorf("kubernetes candidate count exceeds %d", maxCandidates)
	}
	return append(contexts, pods...), nil
}
