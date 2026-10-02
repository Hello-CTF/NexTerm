package docker

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/moby/moby/api/types/registry"
)

func TestStaticRegistryAuth(t *testing.T) {
	provider := NewStaticRegistryAuth(map[string]registry.AuthConfig{
		"registry.local:5000": {Username: "robot", Password: "secret", ServerAddress: "registry.local:5000"},
	})
	encoded, err := provider.RegistryAuth(t.Context(), "session", "registry.local:5000/team/app:v1")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var config registry.AuthConfig
	if err := json.Unmarshal(decoded, &config); err != nil {
		t.Fatal(err)
	}
	if config.Username != "robot" || config.Password != "secret" {
		t.Fatalf("unexpected auth config: %+v", config)
	}
	anonymous, err := provider.RegistryAuth(t.Context(), "session", "library/alpine")
	if err != nil || anonymous != "" {
		t.Fatalf("anonymous auth = %q, %v", anonymous, err)
	}
}

func TestRegistryHost(t *testing.T) {
	for reference, want := range map[string]string{
		"alpine":                        "https://index.docker.io/v1",
		"library/alpine":                "https://index.docker.io/v1",
		"localhost:5000/team/app":       "localhost:5000",
		"registry.example.com/team/app": "registry.example.com",
	} {
		if got := RegistryHost(reference); got != want {
			t.Errorf("RegistryHost(%q) = %q, want %q", reference, got, want)
		}
	}
}
