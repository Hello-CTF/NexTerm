package provider

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
)

const (
	DefaultContextWindow  = 32_768
	MinContextWindow      = 1_000
	MaxContextWindow      = 2_000_000
	MaxTokensHardLimit    = 32_768
	maxReasoningEffortLen = 64
)

var ErrUnknownPreset = errors.New("unknown AI provider preset")

type ReasoningEffort string

const (
	ReasoningEffortMinimal ReasoningEffort = "minimal"
	ReasoningEffortLow     ReasoningEffort = "low"
	ReasoningEffortMedium  ReasoningEffort = "medium"
	ReasoningEffortHigh    ReasoningEffort = "high"
)

// Kind names the wire protocol of a provider. Empty is the OpenAI-compatible
// chat completions protocol every earlier profile uses.
type Kind string

const (
	KindOpenAI    Kind = ""
	KindAnthropic Kind = "anthropic"
)

// Valid reports whether the kind is one the client can speak.
func (k Kind) Valid() bool {
	return k == KindOpenAI || k == KindAnthropic
}

// ThinkingMode is the extended thinking setting of an Anthropic profile.
// Empty is adaptive thinking, the only mode current Claude models accept;
// budget and off exist for older models that still take them.
type ThinkingMode string

const (
	ThinkingAdaptive ThinkingMode = ""
	ThinkingBudget   ThinkingMode = "budget"
	ThinkingOff      ThinkingMode = "off"
)

// Valid reports whether the mode is one of the known choices.
func (m ThinkingMode) Valid() bool {
	return m == ThinkingAdaptive || m == ThinkingBudget || m == ThinkingOff
}

const (
	MinThinkingBudget     = 1_024
	DefaultThinkingBudget = 8_192
)

type Config struct {
	Kind            Kind            `json:"provider,omitempty"`
	BaseURL         string          `json:"baseUrl"`
	APIKey          string          `json:"apiKey"`
	Model           string          `json:"model"`
	FallbackModel   string          `json:"fallbackModel,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	ReasoningEffort ReasoningEffort `json:"reasoningEffort,omitempty"`
	ContextWindow   uint64          `json:"contextWindow"`
	MaxTokens       *int            `json:"maxTokens,omitempty"`
	Proxy           *string         `json:"proxy"`
	Stream          bool            `json:"stream"`
	Thinking        ThinkingMode    `json:"thinking,omitempty"`
	ThinkingBudget  *int            `json:"thinkingBudget,omitempty"`
}

func DefaultConfig() Config {
	return Config{ContextWindow: DefaultContextWindow, Stream: true}
}

func (c Config) Normalized() Config {
	c.Kind = Kind(strings.TrimSpace(string(c.Kind)))
	if !c.Kind.Valid() {
		c.Kind = KindOpenAI
	}
	if !c.Thinking.Valid() {
		c.Thinking = ThinkingAdaptive
	}
	if c.ThinkingBudget != nil {
		budget := min(max(*c.ThinkingBudget, MinThinkingBudget), MaxTokensHardLimit)
		c.ThinkingBudget = &budget
	}
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.Model = strings.TrimSpace(c.Model)
	c.FallbackModel = strings.TrimSpace(c.FallbackModel)
	if c.FallbackModel == c.Model {
		c.FallbackModel = ""
	}
	if c.Temperature != nil {
		if math.IsNaN(*c.Temperature) || math.IsInf(*c.Temperature, 0) {
			c.Temperature = nil
		} else {
			clamped := min(max(*c.Temperature, 0), 2)
			c.Temperature = &clamped
		}
	}
	c.ReasoningEffort = ReasoningEffort(strings.TrimSpace(string(c.ReasoningEffort)))
	if len(c.ReasoningEffort) > maxReasoningEffortLen {
		c.ReasoningEffort = ""
	}
	c.ContextWindow = min(max(c.ContextWindow, MinContextWindow), MaxContextWindow)
	if c.MaxTokens != nil {
		limit := min(int64(MaxTokensHardLimit), int64(c.ContextWindow/2))
		clamped := int(min(max(int64(*c.MaxTokens), 1), limit))
		c.MaxTokens = &clamped
	}
	if c.Proxy != nil {
		proxy := strings.TrimSpace(*c.Proxy)
		if proxy == "" {
			c.Proxy = nil
		} else {
			c.Proxy = &proxy
		}
	}
	return c
}

func (c *Config) UnmarshalJSON(data []byte) error {
	defaults := DefaultConfig()
	var wire struct {
		Kind            Kind            `json:"provider"`
		BaseURL         string          `json:"baseUrl"`
		APIKey          string          `json:"apiKey"`
		Model           string          `json:"model"`
		FallbackModel   string          `json:"fallbackModel"`
		Temperature     *float64        `json:"temperature"`
		ReasoningEffort ReasoningEffort `json:"reasoningEffort"`
		ContextWindow   *uint64         `json:"contextWindow"`
		MaxTokens       *int            `json:"maxTokens"`
		Proxy           *string         `json:"proxy"`
		Stream          *bool           `json:"stream"`
		Thinking        ThinkingMode    `json:"thinking"`
		ThinkingBudget  *int            `json:"thinkingBudget"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	c.Kind = wire.Kind
	c.Thinking = wire.Thinking
	c.ThinkingBudget = wire.ThinkingBudget
	c.BaseURL = wire.BaseURL
	c.APIKey = wire.APIKey
	c.Model = wire.Model
	c.FallbackModel = wire.FallbackModel
	c.Temperature = wire.Temperature
	c.ReasoningEffort = wire.ReasoningEffort
	c.ContextWindow = defaults.ContextWindow
	if wire.ContextWindow != nil {
		c.ContextWindow = *wire.ContextWindow
	}
	c.MaxTokens = wire.MaxTokens
	c.Proxy = wire.Proxy
	c.Stream = defaults.Stream
	if wire.Stream != nil {
		c.Stream = *wire.Stream
	}
	return nil
}

type Preset struct {
	ID     string `json:"id"`
	Config Config `json:"config"`
}

var presetTable = []Preset{
	{ID: "moonshot", Config: Config{BaseURL: "https://api.moonshot.cn/v1", Model: "kimi-k3", ContextWindow: 1_000_000, Stream: true}},
	{ID: "deepseek", Config: Config{BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-flash", ContextWindow: 1_000_000, Stream: true}},
	{ID: "zhipu", Config: Config{BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4.7-flash", ContextWindow: 200_000, Stream: true}},
	{ID: "ollama", Config: Config{BaseURL: "http://127.0.0.1:11434/v1", Model: "qwen2.5:7b", ContextWindow: 32_000, Stream: true}},
	{ID: "anthropic", Config: Config{Kind: KindAnthropic, BaseURL: DefaultAnthropicBaseURL, Model: "claude-opus-5-5", ContextWindow: 1_000_000, Stream: true}},
}

func Presets() []Preset {
	result := make([]Preset, len(presetTable))
	copy(result, presetTable)
	return result
}

func FromPreset(id, model string) (Config, error) {
	for _, preset := range presetTable {
		if preset.ID == id {
			config := preset.Config
			if model = strings.TrimSpace(model); model != "" {
				config.Model = model
			}
			return config, nil
		}
	}
	return Config{}, ErrUnknownPreset
}
