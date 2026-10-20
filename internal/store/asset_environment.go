package store

import "strings"

const AssetEnvironmentTagPrefix = "env:"

func AssetEnvironment(row AssetRow) string {
	environment := ""
	for _, tag := range AssetTagValues(row.Tags) {
		normalized := strings.ToLower(strings.TrimSpace(tag))
		prefixed := strings.HasPrefix(normalized, AssetEnvironmentTagPrefix)
		if prefixed {
			normalized = strings.TrimSpace(strings.TrimPrefix(normalized, AssetEnvironmentTagPrefix))
		}
		candidate := canonicalAssetEnvironment(normalized, prefixed)
		if candidate == "" {
			continue
		}
		if candidate == "production" {
			return candidate
		}
		if environment == "" {
			environment = candidate
		}
	}
	return environment
}

func canonicalAssetEnvironment(value string, prefixed bool) string {
	switch value {
	case "production", "prod":
		return "production"
	case "staging", "stage":
		return "staging"
	case "development", "dev":
		return "development"
	case "testing", "test":
		return "testing"
	default:
		if prefixed {
			return value
		}
		return ""
	}
}
