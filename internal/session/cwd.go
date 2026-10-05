package session

// observeCWD scans tab output for OSC 7 cwd reports. Callers must hold the
// tab feed gate: the tracker is not safe for concurrent use.
func (t *Tab) observeCWD(data []byte) (string, bool) {
	cwd, ok := t.cwdTracker.Observe(data)
	if !ok {
		return "", false
	}
	t.mu.Lock()
	changed := t.cwd != cwd
	t.cwd = cwd
	t.mu.Unlock()
	return cwd, changed
}
