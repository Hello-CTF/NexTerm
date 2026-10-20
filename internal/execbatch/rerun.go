package execbatch

// RerunEligible reports whether a finished target status qualifies for a
// failed-target-only rerun. Successful targets are not rerun, and canceled
// targets are excluded on purpose: cancellation is a deliberate user action,
// not a failure to retry.
func RerunEligible(status Status) bool {
	switch status {
	case StatusExitError, StatusTransportError, StatusTimeout:
		return true
	default:
		return false
	}
}

// SelectRerunTargets returns the session IDs of the failed targets of a
// finished batch, preserving the original target order of the results. An
// empty result means the batch has no rerun candidates.
func SelectRerunTargets(results []Result) []string {
	sessionIDs := make([]string, 0, len(results))
	for _, result := range results {
		if RerunEligible(result.Status) {
			sessionIDs = append(sessionIDs, result.SessionID)
		}
	}
	return sessionIDs
}
