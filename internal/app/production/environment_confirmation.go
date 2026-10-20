package production

import (
	"context"
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
	return guard.ProductionEnvironmentNames(targets)
}

// environmentNamesMatch reports whether the typed names equal the required set,
// ignoring order, surrounding space, and letter case.
func environmentNamesMatch(typed, required []string) bool {
	return guard.EnvironmentNamesMatch(typed, required)
}
