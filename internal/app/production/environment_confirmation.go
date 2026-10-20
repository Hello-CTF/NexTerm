package production

import (
	"context"
	"sort"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

// productionEnvironmentClassifier resolves whether an asset environment counts as
// production for the requesting owner: the built-in names plus the owner's
// configured additions. A nil asset store degrades to the built-in names.
type productionEnvironmentClassifier func(environment string) (bool, error)

func (s *terminalCommandService) classifyProductionEnvironments(ctx context.Context) productionEnvironmentClassifier {
	return func(environment string) (bool, error) {
		if strings.TrimSpace(environment) == "" {
			return false, nil
		}
		if s.database == nil {
			return guard.IsBuiltInProductionEnvironment(environment), nil
		}
		return guard.IsProductionEnvironment(ctx, s.database, environment)
	}
}

// productionEnvironmentNames returns the sorted distinct environment names of the
// production-classified targets; these are the names a confirmation must type back.
func productionEnvironmentNames(targets []string) []string {
	seen := make(map[string]struct{}, len(targets))
	names := make([]string, 0, len(targets))
	for _, environment := range targets {
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

// environmentNamesMatch reports whether the typed names equal the required set,
// ignoring order, surrounding space, and letter case.
func environmentNamesMatch(typed, required []string) bool {
	if len(typed) != len(required) {
		return false
	}
	remaining := append([]string(nil), required...)
	for _, name := range typed {
		matched := false
		for index, candidate := range remaining {
			if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(candidate)) {
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
