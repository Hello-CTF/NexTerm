package logtail

import (
	"fmt"
	"strings"
)

func sourceCommand(source Source) (string, error) {
	value := source.Value
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%w: source value is required", ErrInvalid)
	}
	if len(value) > MaxSourceValueSize || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("%w: source value must be one line of at most %d bytes without NUL", ErrInvalid, MaxSourceValueSize)
	}
	quoted := quoteShell(value)
	switch source.Kind {
	case SourceFile:
		return "tail -n 200 -f -- " + quoted, nil
	case SourceJournal:
		return "journalctl -u " + quoted + " -f --no-pager", nil
	case SourceDocker:
		return "docker logs -f --tail 200 -- " + quoted, nil
	default:
		return "", fmt.Errorf("%w: unknown source kind %q", ErrInvalid, source.Kind)
	}
}

func quoteShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func validateRequest(request Request) (int, error) {
	if request.RunID == "" || len(request.RunID) > 128 || strings.TrimSpace(request.RunID) != request.RunID {
		return 0, fmt.Errorf("%w: runId must be 1-128 non-space-padded characters", ErrInvalid)
	}
	if len(request.Sources) == 0 || len(request.Sources) > MaxSources {
		return 0, fmt.Errorf("%w: sources must contain 1-%d entries", ErrInvalid, MaxSources)
	}
	if request.MaxConcurrent < 0 || request.MaxConcurrent > MaxConcurrency {
		return 0, fmt.Errorf("%w: maxConcurrent must be between 0 and %d", ErrInvalid, MaxConcurrency)
	}
	concurrency := request.MaxConcurrent
	if concurrency == 0 {
		concurrency = DefaultConcurrency
	}
	sourceIDs := make(map[string]struct{}, len(request.Sources))
	sessionIDs := make(map[string]struct{}, len(request.Sources))
	for _, source := range request.Sources {
		if source.SourceID == "" || len(source.SourceID) > 128 || strings.TrimSpace(source.SourceID) != source.SourceID {
			return 0, fmt.Errorf("%w: sourceId must be 1-128 non-space-padded characters", ErrInvalid)
		}
		if strings.TrimSpace(source.SessionID) == "" {
			return 0, fmt.Errorf("%w: sessionId is required", ErrInvalid)
		}
		if _, exists := sourceIDs[source.SourceID]; exists {
			return 0, fmt.Errorf("%w: duplicate sourceId %s", ErrInvalid, source.SourceID)
		}
		if _, exists := sessionIDs[source.SessionID]; exists {
			return 0, fmt.Errorf("%w: duplicate sessionId %s", ErrInvalid, source.SessionID)
		}
		sourceIDs[source.SourceID] = struct{}{}
		sessionIDs[source.SessionID] = struct{}{}
		if _, err := sourceCommand(source); err != nil {
			return 0, err
		}
	}
	return concurrency, nil
}
