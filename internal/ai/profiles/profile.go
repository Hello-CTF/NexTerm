package profiles

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
)

type Profile struct {
	ID              string                   `json:"id"`
	Name            string                   `json:"name"`
	BaseURL         string                   `json:"baseUrl"`
	APIKey          string                   `json:"apiKey"`
	Model           string                   `json:"model"`
	FallbackModel   string                   `json:"fallbackModel,omitempty"`
	Temperature     *float64                 `json:"temperature,omitempty"`
	ReasoningEffort provider.ReasoningEffort `json:"reasoningEffort,omitempty"`
	ContextWindow   uint64                   `json:"contextWindow"`
	MaxTokens       *int                     `json:"maxTokens,omitempty"`
	Proxy           *string                  `json:"proxy"`
	Stream          bool                     `json:"stream"`

	RequestTimeoutSeconds *int `json:"requestTimeoutSeconds,omitempty"`
	IdleTimeoutSeconds    *int `json:"idleTimeoutSeconds,omitempty"`

	CircuitFailureThreshold *int `json:"circuitFailureThreshold,omitempty"`
	CircuitCooldownSeconds  *int `json:"circuitCooldownSeconds,omitempty"`
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
	maxRequestTimeoutSeconds  = 3600
	maxIdleTimeoutSeconds     = 3600
	maxCircuitThreshold       = 100
	maxCircuitCooldownSeconds = 3600
)

func DefaultProfile() Profile {
	return Profile{
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
	p.ReasoningEffort = config.ReasoningEffort
	p.ContextWindow = config.ContextWindow
	p.MaxTokens = config.MaxTokens
	p.Proxy = config.Proxy
	p.RequestTimeoutSeconds = clampOptionalInt(p.RequestTimeoutSeconds, 1, maxRequestTimeoutSeconds)
	p.IdleTimeoutSeconds = clampOptionalInt(p.IdleTimeoutSeconds, 0, maxIdleTimeoutSeconds)
	p.CircuitFailureThreshold = clampOptionalInt(p.CircuitFailureThreshold, 1, maxCircuitThreshold)
	p.CircuitCooldownSeconds = clampOptionalInt(p.CircuitCooldownSeconds, 1, maxCircuitCooldownSeconds)
	if p.Name == "" {
		p.Name = p.Model
		if p.Name == "" {
			p.Name = "未命名模型"
		}
	}
	return p
}

func clampOptionalInt(value *int, minimum, maximum int) *int {
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
		Temperature: cloneFloat64(p.Temperature), ReasoningEffort: p.ReasoningEffort, ContextWindow: p.ContextWindow,
		MaxTokens: cloneInt(p.MaxTokens),
		Proxy:     cloneString(p.Proxy), Stream: p.Stream,
	}
}

func (p Profile) CircuitConfig() (int, time.Duration) {
	threshold := provider.DefaultCircuitThreshold
	if p.CircuitFailureThreshold != nil {
		threshold = *p.CircuitFailureThreshold
	}
	cooldown := provider.DefaultCircuitCooldown
	if p.CircuitCooldownSeconds != nil {
		cooldown = time.Duration(*p.CircuitCooldownSeconds) * time.Second
	}
	return threshold, cooldown
}

func (p *Profile) UnmarshalJSON(data []byte) error {
	defaults := DefaultProfile()
	var wire struct {
		ID              string                   `json:"id"`
		Name            string                   `json:"name"`
		BaseURL         string                   `json:"baseUrl"`
		APIKey          string                   `json:"apiKey"`
		Model           string                   `json:"model"`
		FallbackModel   string                   `json:"fallbackModel"`
		Temperature     *float64                 `json:"temperature"`
		ReasoningEffort provider.ReasoningEffort `json:"reasoningEffort"`
		ContextWindow   *uint64                  `json:"contextWindow"`
		MaxTokens       *int                     `json:"maxTokens"`
		Proxy           *string                  `json:"proxy"`
		Stream          *bool                    `json:"stream"`

		RequestTimeoutSeconds *int `json:"requestTimeoutSeconds"`
		IdleTimeoutSeconds    *int `json:"idleTimeoutSeconds"`

		CircuitFailureThreshold *int `json:"circuitFailureThreshold"`
		CircuitCooldownSeconds  *int `json:"circuitCooldownSeconds"`
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
	p.Temperature = wire.Temperature
	p.ReasoningEffort = wire.ReasoningEffort
	p.ContextWindow = defaults.ContextWindow
	if wire.ContextWindow != nil {
		p.ContextWindow = *wire.ContextWindow
	}
	p.MaxTokens = wire.MaxTokens
	p.Proxy = wire.Proxy
	p.Stream = defaults.Stream
	if wire.Stream != nil {
		p.Stream = *wire.Stream
	}
	p.RequestTimeoutSeconds = wire.RequestTimeoutSeconds
	p.IdleTimeoutSeconds = wire.IdleTimeoutSeconds
	p.CircuitFailureThreshold = wire.CircuitFailureThreshold
	p.CircuitCooldownSeconds = wire.CircuitCooldownSeconds
	return nil
}

func PresetProfile(id string) (Profile, error) {
	config, err := provider.FromPreset(id, "")
	if err != nil {
		return Profile{}, fmt.Errorf("%w: %s", err, id)
	}
	return Profile{
		Name: id, BaseURL: config.BaseURL, Model: config.Model,
		Temperature: cloneFloat64(config.Temperature), ReasoningEffort: config.ReasoningEffort, ContextWindow: config.ContextWindow,
		Proxy: config.Proxy, Stream: config.Stream,
	}, nil
}

type Overview struct {
	Profiles []Profile `json:"profiles"`
	ActiveID *string   `json:"activeId"`
}

func cloneProfile(profile Profile) Profile {
	profile.Proxy = cloneString(profile.Proxy)
	profile.Temperature = cloneFloat64(profile.Temperature)
	profile.MaxTokens = cloneInt(profile.MaxTokens)
	profile.RequestTimeoutSeconds = cloneInt(profile.RequestTimeoutSeconds)
	profile.IdleTimeoutSeconds = cloneInt(profile.IdleTimeoutSeconds)
	profile.CircuitFailureThreshold = cloneInt(profile.CircuitFailureThreshold)
	profile.CircuitCooldownSeconds = cloneInt(profile.CircuitCooldownSeconds)
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

func cloneFloat64(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
