package provider

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
)

const (
	DefaultTemperature   = 0.3
	DefaultContextWindow = 32_768
	MinContextWindow     = 1_000
	MaxContextWindow     = 2_000_000
)

var ErrUnknownPreset = errors.New("unknown AI provider preset")

type Config struct {
	BaseURL       string  `json:"baseUrl"`
	APIKey        string  `json:"apiKey"`
	Model         string  `json:"model"`
	FallbackModel string  `json:"fallbackModel,omitempty"`
	Temperature   float64 `json:"temperature"`
	ContextWindow uint64  `json:"contextWindow"`
	Proxy         *string `json:"proxy"`
	Stream        bool    `json:"stream"`
}

func DefaultConfig() Config {
	return Config{Temperature: DefaultTemperature, ContextWindow: DefaultContextWindow, Stream: true}
}

func (c Config) Normalized() Config {
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.Model = strings.TrimSpace(c.Model)
	c.FallbackModel = strings.TrimSpace(c.FallbackModel)
	if c.FallbackModel == c.Model {
		c.FallbackModel = ""
	}
	if math.IsNaN(c.Temperature) || math.IsInf(c.Temperature, 0) {
		c.Temperature = DefaultTemperature
	} else {
		c.Temperature = min(max(c.Temperature, 0), 2)
	}
	c.ContextWindow = min(max(c.ContextWindow, MinContextWindow), MaxContextWindow)
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
		BaseURL       string   `json:"baseUrl"`
		APIKey        string   `json:"apiKey"`
		Model         string   `json:"model"`
		FallbackModel string   `json:"fallbackModel"`
		Temperature   *float64 `json:"temperature"`
		ContextWindow *uint64  `json:"contextWindow"`
		Proxy         *string  `json:"proxy"`
		Stream        *bool    `json:"stream"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	c.BaseURL = wire.BaseURL
	c.APIKey = wire.APIKey
	c.Model = wire.Model
	c.FallbackModel = wire.FallbackModel
	c.Temperature = defaults.Temperature
	if wire.Temperature != nil {
		c.Temperature = *wire.Temperature
	}
	c.ContextWindow = defaults.ContextWindow
	if wire.ContextWindow != nil {
		c.ContextWindow = *wire.ContextWindow
	}
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
	{ID: "deepseek", Config: Config{BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat", Temperature: DefaultTemperature, ContextWindow: 64_000, Stream: true}},
	{ID: "openai", Config: Config{BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini", Temperature: DefaultTemperature, ContextWindow: 128_000, Stream: true}},
	{ID: "dashscope", Config: Config{BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen-plus", Temperature: DefaultTemperature, ContextWindow: 128_000, Stream: true}},
	{ID: "moonshot", Config: Config{BaseURL: "https://api.moonshot.cn/v1", Model: "moonshot-v1-32k", Temperature: DefaultTemperature, ContextWindow: 128_000, Stream: true}},
	{ID: "zhipu", Config: Config{BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4-flash", Temperature: DefaultTemperature, ContextWindow: 128_000, Stream: true}},
	{ID: "ollama", Config: Config{BaseURL: "http://127.0.0.1:11434/v1", Model: "qwen2.5:7b", Temperature: DefaultTemperature, ContextWindow: 32_000, Stream: true}},
	{ID: "lmstudio", Config: Config{BaseURL: "http://127.0.0.1:1234/v1", Model: "local-model", Temperature: DefaultTemperature, ContextWindow: 32_000, Stream: true}},
	{ID: "vllm", Config: Config{BaseURL: "http://127.0.0.1:8000/v1", Model: "local-model", Temperature: DefaultTemperature, ContextWindow: 32_000, Stream: true}},
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
