package profiles

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
)

type Profile struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	BaseURL       string  `json:"baseUrl"`
	APIKey        string  `json:"apiKey"`
	Model         string  `json:"model"`
	FallbackModel string  `json:"fallbackModel,omitempty"`
	Temperature   float64 `json:"temperature"`
	ContextWindow uint64  `json:"contextWindow"`
	Proxy         *string `json:"proxy"`
	Stream        bool    `json:"stream"`

	RequestTimeoutSeconds *int `json:"requestTimeoutSeconds,omitempty"`
	IdleTimeoutSeconds    *int `json:"idleTimeoutSeconds,omitempty"`
}

const MaskedAPIKey = "••••••••••••"

func MaskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	return MaskedAPIKey
}

func RejectMaskedKey(key string) error {
	if key == MaskedAPIKey {
		return fmt.Errorf("%w: masked sentinel", ErrProfileKeyUnavailable)
	}
	return nil
}

func (p Profile) hasKeyMaterial() bool {
	return p.APIKey != ""
}

const (
	maxRequestTimeoutSeconds = 3600
	maxIdleTimeoutSeconds    = 3600
)

func DefaultProfile() Profile {
	return Profile{
		Temperature:   provider.DefaultTemperature,
		ContextWindow: provider.DefaultContextWindow,
		Stream:        true,
	}
}

func (p Profile) Normalized() Profile {
	config := p.ProviderConfig().Normalized()
	p.ID = strings.TrimSpace(p.ID)
	p.Name = strings.TrimSpace(p.Name)
	p.BaseURL = config.BaseURL
	p.APIKey = config.APIKey
	p.Model = config.Model
	p.FallbackModel = config.FallbackModel
	p.Temperature = config.Temperature
	p.ContextWindow = config.ContextWindow
	p.Proxy = config.Proxy
	p.RequestTimeoutSeconds = clampTimeoutSeconds(p.RequestTimeoutSeconds, 1, maxRequestTimeoutSeconds)
	p.IdleTimeoutSeconds = clampTimeoutSeconds(p.IdleTimeoutSeconds, 0, maxIdleTimeoutSeconds)
	if p.Name == "" {
		p.Name = p.Model
		if p.Name == "" {
			p.Name = "未命名模型"
		}
	}
	return p
}

func clampTimeoutSeconds(value *int, minimum, maximum int) *int {
	if value == nil {
		return nil
	}
	clamped := min(max(*value, minimum), maximum)
	return &clamped
}

func (p Profile) ClientOptions() []provider.Option {
	var options []provider.Option
	if p.RequestTimeoutSeconds != nil && *p.RequestTimeoutSeconds > 0 {
		timeout := time.Duration(*p.RequestTimeoutSeconds) * time.Second
		options = append(options, provider.WithTimeouts(provider.Timeouts{Stream: timeout, Block: timeout}))
	}
	if p.IdleTimeoutSeconds != nil {
		options = append(options, provider.WithIdleTimeout(time.Duration(*p.IdleTimeoutSeconds)*time.Second))
	}
	return options
}

func (p Profile) ProviderConfig() provider.Config {
	return provider.Config{
		BaseURL: p.BaseURL, APIKey: p.APIKey, Model: p.Model, FallbackModel: p.FallbackModel,
		Temperature: p.Temperature, ContextWindow: p.ContextWindow,
		Proxy: cloneString(p.Proxy), Stream: p.Stream,
	}
}

func (p *Profile) UnmarshalJSON(data []byte) error {
	defaults := DefaultProfile()
	var wire struct {
		ID            string   `json:"id"`
		Name          string   `json:"name"`
		BaseURL       string   `json:"baseUrl"`
		APIKey        string   `json:"apiKey"`
		Model         string   `json:"model"`
		FallbackModel string   `json:"fallbackModel"`
		Temperature   *float64 `json:"temperature"`
		ContextWindow *uint64  `json:"contextWindow"`
		Proxy         *string  `json:"proxy"`
		Stream        *bool    `json:"stream"`

		RequestTimeoutSeconds *int `json:"requestTimeoutSeconds"`
		IdleTimeoutSeconds    *int `json:"idleTimeoutSeconds"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	p.ID = wire.ID
	p.Name = wire.Name
	p.BaseURL = wire.BaseURL
	p.APIKey = wire.APIKey
	p.Model = wire.Model
	p.FallbackModel = wire.FallbackModel
	p.Temperature = defaults.Temperature
	if wire.Temperature != nil {
		p.Temperature = *wire.Temperature
	}
	p.ContextWindow = defaults.ContextWindow
	if wire.ContextWindow != nil {
		p.ContextWindow = *wire.ContextWindow
	}
	p.Proxy = wire.Proxy
	p.Stream = defaults.Stream
	if wire.Stream != nil {
		p.Stream = *wire.Stream
	}
	p.RequestTimeoutSeconds = wire.RequestTimeoutSeconds
	p.IdleTimeoutSeconds = wire.IdleTimeoutSeconds
	return nil
}

func PresetProfile(id string) (Profile, error) {
	config, err := provider.FromPreset(id, "")
	if err != nil {
		return Profile{}, fmt.Errorf("%w: %s", err, id)
	}
	return Profile{
		Name: id, BaseURL: config.BaseURL, Model: config.Model,
		Temperature: config.Temperature, ContextWindow: config.ContextWindow,
		Proxy: config.Proxy, Stream: config.Stream,
	}, nil
}

type Overview struct {
	Profiles []Profile `json:"profiles"`
	ActiveID *string   `json:"activeId"`
}

func cloneProfile(profile Profile) Profile {
	profile.Proxy = cloneString(profile.Proxy)
	profile.RequestTimeoutSeconds = cloneInt(profile.RequestTimeoutSeconds)
	profile.IdleTimeoutSeconds = cloneInt(profile.IdleTimeoutSeconds)
	return profile
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
