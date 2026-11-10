package session

// TabCount is the number of live tabs of one asset kind and persistence.
type TabCount struct {
	Kind       string
	Persistent bool
	Count      int
}

// TabCounts reports the live (not exited, not closed) tabs of every owner,
// grouped by asset kind and whether a persistent terminal backs them.
func (m *Manager) TabCounts() []TabCount {
	m.mu.Lock()
	tabs := make([]*Tab, 0, len(m.tabs))
	for _, tab := range m.tabs {
		tabs = append(tabs, tab)
	}
	m.mu.Unlock()
	type key struct {
		kind       string
		persistent bool
	}
	counts := make(map[key]int)
	var order []key
	for _, tab := range tabs {
		kind := ""
		if tab.session != nil {
			kind = tab.session.asset.Kind
		}
		tab.mu.Lock()
		live := !tab.exited && !tab.closed
		persistent := tab.durable != nil
		tab.mu.Unlock()
		if !live {
			continue
		}
		k := key{kind: kind, persistent: persistent}
		if _, seen := counts[k]; !seen {
			order = append(order, k)
		}
		counts[k]++
	}
	result := make([]TabCount, 0, len(order))
	for _, k := range order {
		result = append(result, TabCount{Kind: k.kind, Persistent: k.persistent, Count: counts[k]})
	}
	return result
}
