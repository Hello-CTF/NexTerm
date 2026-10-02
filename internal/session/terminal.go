package session

import (
	"context"

	"github.com/ProbiusOfficial/NexTerm/internal/terminal"
)

type coreTerminal struct {
	*terminal.Tab
	output *terminal.Output
}

func (t *coreTerminal) Feed(raw []byte) {
	_ = t.output.Chunk(context.Background(), raw)
}

func (t *coreTerminal) Close() {
	t.StopRecording()
	t.output.Close()
	t.Tab.Close()
}

type terminalFactory struct{}

func (terminalFactory) NewTerminal(config TerminalConfig) (TerminalState, error) {
	encoding := terminal.UTF8
	var err error
	if config.Encoding != "" {
		encoding, err = terminal.ParseEncoding(config.Encoding)
		if err != nil {
			return nil, err
		}
	}
	tab := terminal.NewTab(config.TabID, config.SessionID, int(config.Cols), int(config.Rows), encoding)
	return &coreTerminal{Tab: tab, output: terminal.NewOutput(tab)}, nil
}

func (m *Manager) TerminalSnapshot(tabID string) (terminal.Snapshot, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return terminal.Snapshot{}, err
	}
	core, ok := tab.terminal.(interface{ Snapshot() terminal.Snapshot })
	if !ok {
		return terminal.Snapshot{}, ErrUnsupported
	}
	return core.Snapshot(), nil
}

func (m *Manager) ScreenText(tabID string) (string, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return "", err
	}
	core, ok := tab.terminal.(interface{ ScreenText() string })
	if !ok {
		return "", ErrUnsupported
	}
	return core.ScreenText(), nil
}

func (m *Manager) TailLines(tabID string, count int) ([]string, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return nil, err
	}
	core, ok := tab.terminal.(interface{ TailLines(int) []string })
	if !ok {
		return nil, ErrUnsupported
	}
	return core.TailLines(count), nil
}

func (m *Manager) TerminalModeState(tabID string) (terminal.State, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return terminal.State{}, err
	}
	core, ok := tab.terminal.(interface{ State() terminal.State })
	if !ok {
		return terminal.State{}, ErrUnsupported
	}
	return core.State(), nil
}

func (m *Manager) RawDump(tabID string, maxBytes int) ([]byte, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return nil, err
	}
	return tab.terminal.Dump(maxBytes), nil
}

func (m *Manager) ExportLog(tabID, path string, maxBytes int) (uint64, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return 0, err
	}
	core, ok := tab.terminal.(interface {
		ExportLog(string, int) (uint64, error)
	})
	if !ok {
		return 0, ErrUnsupported
	}
	return core.ExportLog(path, maxBytes)
}

func (m *Manager) SwitchEncoding(tabID, encoding string) error {
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	parsed, err := terminal.ParseEncoding(encoding)
	if err != nil {
		return err
	}
	core, ok := tab.terminal.(interface{ SwitchEncoding(terminal.Encoding) })
	if !ok {
		return ErrUnsupported
	}
	core.SwitchEncoding(parsed)
	return nil
}

func (m *Manager) SetVisible(tabID string, visible bool) error {
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	core, ok := tab.terminal.(interface{ SetVisible(bool) })
	if !ok {
		return ErrUnsupported
	}
	core.SetVisible(visible)
	return nil
}

func (m *Manager) StartRecording(tabID, path string) error {
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	core, ok := tab.terminal.(interface{ StartRecording(string) error })
	if !ok {
		return ErrUnsupported
	}
	return core.StartRecording(path)
}

func (m *Manager) StopRecording(tabID string) (uint64, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return 0, err
	}
	core, ok := tab.terminal.(interface{ StopRecording() uint64 })
	if !ok {
		return 0, ErrUnsupported
	}
	return core.StopRecording(), nil
}

func (m *Manager) RecordingBytes(tabID string) (uint64, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return 0, err
	}
	core, ok := tab.terminal.(interface{ RecordingBytes() uint64 })
	if !ok {
		return 0, ErrUnsupported
	}
	return core.RecordingBytes(), nil
}

func (m *Manager) InjectInternal(ctx context.Context, tabID string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	tab.mu.Lock()
	if tab.closed {
		tab.mu.Unlock()
		return ErrTabClosed
	}
	generation := tab.generation
	tab.mu.Unlock()
	return m.feed(ctx, tab, generation, data)
}

func (m *Manager) WriteInternal(ctx context.Context, tabID string, data []byte) error {
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	tab.writeMu.Lock()
	defer tab.writeMu.Unlock()
	tab.mu.Lock()
	if tab.closed {
		tab.mu.Unlock()
		return ErrTabClosed
	}
	if !ptyBacked(tab.session.asset.Kind) {
		tab.mu.Unlock()
		return ErrUnsupported
	}
	channel := tab.channel
	generation := tab.generation
	exited := tab.exited
	tab.mu.Unlock()
	tab.session.mu.Lock()
	connected := tab.session.status == StatusConnected && tab.session.generation == generation
	tab.session.mu.Unlock()
	if !connected {
		return ErrDisconnected
	}
	if exited || channel == nil {
		return ErrTabClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeAll(channel, data)
}
