package usage

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
)

type Usage struct {
	RunID               string `json:"runId,omitempty"`
	CallID              string `json:"callId,omitempty"`
	Model               string `json:"model,omitempty"`
	PromptTokens        uint64 `json:"promptTokens"`
	CompletionTokens    uint64 `json:"completionTokens"`
	CachedTokens        uint64 `json:"cachedTokens"`
	CacheCreationTokens uint64 `json:"cacheCreationTokens,omitempty"`
	LatencyMS           int64  `json:"latencyMs,omitempty"`
	ContextWindow       uint64 `json:"contextWindow"`
	mixedModels         bool
	mixedRunIDs         bool
	mixedCallIDs        bool
}

func Parse(raw json.RawMessage) Usage {
	if len(raw) == 0 {
		return Usage{}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return Usage{}
	}
	root, ok := value.(map[string]any)
	if !ok {
		return Usage{}
	}

	prompt, promptSet := firstUint(root, []string{"prompt_tokens"}, []string{"input_tokens"})
	completion, _ := firstUint(root, []string{"completion_tokens"}, []string{"output_tokens"})
	cached, _ := firstUint(root,
		[]string{"prompt_tokens_details", "cached_tokens"},
		[]string{"input_tokens_details", "cached_tokens"},
		[]string{"prompt_cache_hit_tokens"},
		[]string{"cached_tokens"},
		[]string{"cache_read_input_tokens"},
	)
	cacheCreation, _ := firstUint(root,
		[]string{"prompt_tokens_details", "cache_creation_tokens"},
		[]string{"input_tokens_details", "cache_creation_tokens"},
		[]string{"cache_creation_input_tokens"},
		[]string{"prompt_cache_creation_tokens"},
		[]string{"cache_creation", "tokens"},
	)

	hit, hitSet := uintAt(root, []string{"prompt_cache_hit_tokens"})
	miss, missSet := uintAt(root, []string{"prompt_cache_miss_tokens"})
	if !promptSet && (hitSet || missSet) {
		prompt = saturatingAdd(hit, miss)
		promptSet = true
	}
	if promptSet && cached > prompt {
		cached = prompt
	}
	return Usage{PromptTokens: prompt, CompletionTokens: completion, CachedTokens: cached, CacheCreationTokens: cacheCreation}
}

func (u Usage) HasData() bool {
	return u.PromptTokens > 0 || u.CompletionTokens > 0
}

func (u Usage) ContextPercent() float64 {
	if u.ContextWindow == 0 {
		return 0
	}
	percent := float64(u.PromptTokens) / float64(u.ContextWindow) * 100
	return math.Min(100, math.Max(0, percent))
}

func (u Usage) CacheHitPercent() float64 {
	if u.PromptTokens == 0 {
		return 0
	}
	cached := min(u.CachedTokens, u.PromptTokens)
	return float64(cached) / float64(u.PromptTokens) * 100
}

func (u *Usage) Accumulate(other Usage) {
	u.PromptTokens = saturatingAdd(u.PromptTokens, other.PromptTokens)
	u.CompletionTokens = saturatingAdd(u.CompletionTokens, other.CompletionTokens)
	u.CachedTokens = saturatingAdd(u.CachedTokens, other.CachedTokens)
	u.CacheCreationTokens = saturatingAdd(u.CacheCreationTokens, other.CacheCreationTokens)
	if other.LatencyMS > 0 {
		u.LatencyMS += other.LatencyMS
	}
	if other.ContextWindow != 0 {
		u.ContextWindow = other.ContextWindow
	}
	mergeUsageIdentity(&u.Model, &u.mixedModels, other.Model, other.mixedModels)
	mergeUsageIdentity(&u.RunID, &u.mixedRunIDs, other.RunID, other.mixedRunIDs)
	mergeUsageIdentity(&u.CallID, &u.mixedCallIDs, other.CallID, other.mixedCallIDs)
}

func mergeUsageIdentity(current *string, mixed *bool, other string, otherMixed bool) {
	if otherMixed {
		*current = ""
		*mixed = true
		return
	}
	if other == "" || *mixed {
		return
	}
	if *current == "" {
		*current = other
	} else if *current != other {
		*current = ""
		*mixed = true
	}
}

func firstUint(root map[string]any, paths ...[]string) (uint64, bool) {
	for _, path := range paths {
		if value, ok := uintAt(root, path); ok {
			return value, true
		}
	}
	return 0, false
}

func uintAt(root map[string]any, path []string) (uint64, bool) {
	var current any = root
	for _, part := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return 0, false
		}
		current, ok = object[part]
		if !ok {
			return 0, false
		}
	}
	switch value := current.(type) {
	case json.Number:
		parsed, err := strconv.ParseUint(value.String(), 10, 64)
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseUint(value, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func saturatingAdd(left, right uint64) uint64 {
	if math.MaxUint64-left < right {
		return math.MaxUint64
	}
	return left + right
}

func SaturatingAdd(left, right uint64) uint64 {
	return saturatingAdd(left, right)
}

func SaturatingInt64(value uint64) int64 {
	const maxInt64 = uint64(^uint64(0) >> 1)
	if value > maxInt64 {
		return int64(maxInt64)
	}
	return int64(value)
}
