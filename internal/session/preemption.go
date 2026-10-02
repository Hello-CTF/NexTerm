package session

func (m *Manager) SetUserInputHook(hook func(tabID string)) {
	m.hookMu.Lock()
	m.userInputHook = hook
	m.hookMu.Unlock()
}

func (m *Manager) notifyUserInput(tabID string) {
	m.hookMu.RLock()
	hook := m.userInputHook
	m.hookMu.RUnlock()
	if hook != nil {
		hook(tabID)
	}
}
