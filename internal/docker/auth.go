package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"

	"github.com/moby/moby/api/types/registry"
)

func EncodeRegistryAuth(config registry.AuthConfig) (string, error) {
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(encoded), nil
}

type StaticRegistryAuth struct {
	mu          sync.RWMutex
	credentials map[string]registry.AuthConfig
}

func NewStaticRegistryAuth(credentials map[string]registry.AuthConfig) *StaticRegistryAuth {
	provider := &StaticRegistryAuth{credentials: make(map[string]registry.AuthConfig)}
	for server, config := range credentials {
		provider.Set(server, config)
	}
	return provider
}

func (p *StaticRegistryAuth) Set(server string, config registry.AuthConfig) {
	p.mu.Lock()
	p.credentials[strings.TrimSuffix(server, "/")] = config
	p.mu.Unlock()
}

func (p *StaticRegistryAuth) RegistryAuth(_ context.Context, _ string, reference string) (string, error) {
	host := RegistryHost(reference)
	p.mu.RLock()
	config, ok := p.credentials[strings.TrimSuffix(host, "/")]
	if !ok {
		config, ok = p.credentials["*"]
	}
	p.mu.RUnlock()
	if !ok {
		return "", nil
	}
	return EncodeRegistryAuth(config)
}

func RegistryHost(reference string) string {
	first, rest, found := strings.Cut(reference, "/")
	if found && rest != "" && (strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost") {
		return first
	}
	return "https://index.docker.io/v1"
}
