package sync

type groupTopologyIssue uint8

const (
	groupTopologyOK groupTopologyIssue = iota
	groupTopologyCycle
	groupTopologyTooDeep
)

func mergedGroupTopology(groupID string, parentID *string, parents map[string]*string) groupTopologyIssue {
	seen := map[string]bool{groupID: true}
	for cursor, depth := parentID, 0; cursor != nil && *cursor != ""; depth++ {
		if depth >= 64 {
			return groupTopologyTooDeep
		}
		id := *cursor
		if seen[id] {
			return groupTopologyCycle
		}
		seen[id] = true
		next, exists := parents[id]
		if !exists {
			break
		}
		cursor = next
	}
	return groupTopologyOK
}
