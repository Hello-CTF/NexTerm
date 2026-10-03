package takeover

import "context"

func (m *Manager) Start(context.Context) error {
	return nil
}

func (m *Manager) Shutdown(ctx context.Context) error {
	return m.CloseContext(ctx)
}
