package guard

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// ProductionEnvironmentsSettingKey persists the per-owner list of additional
// environment names that count as production on top of the built-in names.
const ProductionEnvironmentsSettingKey = "ai.production_environments"

func ProductionEnvironmentsSettingKeyForUser(ctx context.Context) string {
	if userID, ok := ipc.UserIDFromContext(ctx); ok {
		return ProductionEnvironmentsSettingKey + ".user." + userID
	}
	return ProductionEnvironmentsSettingKey
}

// builtInProductionEnvironments always count as production; the configured
// list adds to them, so a configuration mistake cannot unguard production.
var builtInProductionEnvironments = []string{"production", "prod"}

// ManagedDeviceEnvironment is the environment recorded for managed-device
// targets. Devices carry no environment tag, so batch execution treats them as
// production-equivalent: the owner must confirm before anything runs.
const ManagedDeviceEnvironment = "managed-device"

// IsBuiltInProductionEnvironment reports whether the environment is one of the
// built-in production names.
func IsBuiltInProductionEnvironment(environment string) bool {
	for _, name := range builtInProductionEnvironments {
		if environmentMatches(name, environment) {
			return true
		}
	}
	return false
}

// NormalizeProductionEnvironments trims, lowercases, and deduplicates
// environment names, dropping empty entries.
func NormalizeProductionEnvironments(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		name := strings.ToLower(strings.TrimSpace(value))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	return normalized
}

// ConfiguredProductionEnvironments reads the owner's additional production
// environment names; a missing or empty setting means no additions.
func ConfiguredProductionEnvironments(ctx context.Context, settings Settings) ([]string, error) {
	if settings == nil {
		return nil, nil
	}
	raw, found, err := settings.SettingGet(ctx, ProductionEnvironmentsSettingKeyForUser(ctx))
	if err != nil {
		return nil, err
	}
	if !found || raw == "" {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	return NormalizeProductionEnvironments(values), nil
}

// SetProductionEnvironments persists the owner's additional production
// environment names, replacing any previous configuration.
func SetProductionEnvironments(ctx context.Context, settings Settings, values []string) error {
	normalized := NormalizeProductionEnvironments(values)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	return settings.SettingSet(ctx, ProductionEnvironmentsSettingKeyForUser(ctx), string(encoded))
}

// IsProductionEnvironment reports whether the environment counts as production
// for the owner in ctx: the built-in names plus the configured additions. A
// nil settings reader degrades to the built-in names only.
func IsProductionEnvironment(ctx context.Context, settings Settings, environment string) (bool, error) {
	if strings.TrimSpace(environment) == "" {
		return false, nil
	}
	if IsBuiltInProductionEnvironment(environment) || environmentMatches(ManagedDeviceEnvironment, environment) {
		return true, nil
	}
	configured, err := ConfiguredProductionEnvironments(ctx, settings)
	if err != nil {
		return false, err
	}
	for _, name := range configured {
		if environmentMatches(name, environment) {
			return true, nil
		}
	}
	return false, nil
}

func environmentMatches(name, environment string) bool {
	return strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(environment))
}

// ProductionEnvironmentNames returns the sorted distinct non-empty environment
// names of the production-classified targets; these are the names a production
// confirmation must type back.
func ProductionEnvironmentNames(environments []string) []string {
	seen := make(map[string]struct{}, len(environments))
	names := make([]string, 0, len(environments))
	for _, environment := range environments {
		name := strings.TrimSpace(environment)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// EnvironmentNamesMatch reports whether the typed names equal the required set,
// ignoring order, surrounding space, and letter case.
func EnvironmentNamesMatch(typed, required []string) bool {
	if len(typed) != len(required) {
		return false
	}
	remaining := append([]string(nil), required...)
	for _, name := range typed {
		matched := false
		for index, candidate := range remaining {
			if environmentMatches(name, candidate) {
				remaining = append(remaining[:index], remaining[index+1:]...)
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return len(remaining) == 0
}
