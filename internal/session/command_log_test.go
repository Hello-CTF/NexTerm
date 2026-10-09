package session

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

type fakeCommandSink struct {
	mu      sync.Mutex
	records []CommandRecord
}

func (s *fakeCommandSink) RecordCommand(_ context.Context, record CommandRecord) {
	s.mu.Lock()
	s.records = append(s.records, record)
	s.mu.Unlock()
}

func (s *fakeCommandSink) snapshot() []CommandRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CommandRecord(nil), s.records...)
}

func openCommandTestTab(t *testing.T, sink CommandSink, userID string) (*Manager, *Tab, *fakeChannel, *Session) {
	t.Helper()
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector,
		Terminals: terminalFactory{},
		Commands:  sink,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	if userID != "" {
		ctx = ipc.WithUserID(ctx, userID)
	}
	connected, err := manager.Connect(ctx, Asset{ID: "asset-1", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "cmd-channel")
	return manager, tab, connector.transport(0).channel(0), connected
}

// TestCommandLogRecordsLifecycleDrivesAStructuredRecord 走完 OSC 133 全生命
// 周期: 屏幕上的命令行 + 退出码 + 会话/标签/资产/用户归因齐全。
func TestCommandLogRecordsLifecycleDrivesAStructuredRecord(t *testing.T) {
	sink := &fakeCommandSink{}
	_, tab, channel, connected := openCommandTestTab(t, sink, "u-1")

	if err := channel.emit([]byte("user@host:~$ ls -la\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("\x1b]133;B\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.CommandState().Running })
	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("running command must not be recorded yet, got %d records", got)
	}

	if err := channel.emit([]byte("total 0\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("\x1b]133;D;0\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.snapshot()) == 1 })

	record := sink.snapshot()[0]
	if record.Command != "user@host:~$ ls -la" {
		t.Fatalf("command = %q, want the completed screen line", record.Command)
	}
	if record.Source != CommandSourceTerminal {
		t.Fatalf("source = %q, want %q", record.Source, CommandSourceTerminal)
	}
	if record.ExitCode == nil || *record.ExitCode != 0 {
		t.Fatalf("exit code = %v, want 0", record.ExitCode)
	}
	if record.SessionID != connected.ID || record.TabID != tab.ID || record.AssetID != "asset-1" {
		t.Fatalf("attribution = %+v", record)
	}
	if record.UserID != "u-1" {
		t.Fatalf("user id = %q, want u-1 from the connect context", record.UserID)
	}
	if record.StartedAt.After(record.FinishedAt) {
		t.Fatalf("started %v after finished %v", record.StartedAt, record.FinishedAt)
	}
}

// TestCommandLogIgnores133FreeStream 无 OSC 133 的 shell 不产生任何命令
// 记录, 也不记录纯输出。
func TestCommandLogIgnores133FreeStream(t *testing.T) {
	sink := &fakeCommandSink{}
	_, tab, channel, _ := openCommandTestTab(t, sink, "")

	if err := channel.emit([]byte("user@host:~$ ls\r\nfile1\r\n\x1b]7;file://h/home/u\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.Info().Cwd == "/home/u" })
	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("133-free stream produced %d records", got)
	}
}

// TestCommandLogSkipsTerminalWithoutSnapshot 终端不支持屏幕快照时命令观察
// 保持静默, 不编造命令文本。
func TestCommandLogSkipsTerminalWithoutSnapshot(t *testing.T) {
	connector := newFakeConnector()
	sink := &fakeCommandSink{}
	manager := NewManager(Config{
		Connector: connector,
		Terminals: newFakeTerminalFactory(),
		Commands:  sink,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "asset-1", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "cmd-channel")
	channel := connector.transport(0).channel(0)

	if err := channel.emit([]byte("user@host:~$ ls\r\n\x1b]133;B\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.CommandState().Running })
	if err := channel.emit([]byte("\x1b]133;D;1\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !tab.CommandState().Running })
	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("snapshot-less terminal produced %d records", got)
	}
}

// TestCommandLogClosesPreviousWithoutExitOnNewStart 上一条命令丢失结束报告
// 时, 以下一个提示符/命令边界诚实收尾 (退出码未知), 而不是丢弃或张冠李戴。
func TestCommandLogClosesPreviousWithoutExitOnNewStart(t *testing.T) {
	sink := &fakeCommandSink{}
	_, tab, channel, _ := openCommandTestTab(t, sink, "")

	if err := channel.emit([]byte("one\r\n\x1b]133;B\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.CommandState().Running })
	if err := channel.emit([]byte("two\r\n\x1b]133;A\x1b\\\x1b]133;B\x1b\\\x1b]133;D;7\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.snapshot()) == 2 })

	records := sink.snapshot()
	if records[0].Command != "one" || records[0].ExitCode != nil {
		t.Fatalf("first record = %+v, want command one with unknown exit", records[0])
	}
	if records[1].Command != "two" || records[1].ExitCode == nil || *records[1].ExitCode != 7 {
		t.Fatalf("second record = %+v, want command two with exit 7", records[1])
	}
}

// TestCommandLogStartupFinishProducesNoRecord shell 启动提示符自带的
// 133;D 没有对应命令, 不能记成一条命令。
func TestCommandLogStartupFinishProducesNoRecord(t *testing.T) {
	sink := &fakeCommandSink{}
	_, tab, channel, _ := openCommandTestTab(t, sink, "")

	if err := channel.emit([]byte("\x1b]133;D;0\x1b\\\x1b]133;A\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.CommandState().HasLastExitCode })
	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("startup prompt produced %d records", got)
	}
}

// TestCommandLogFlushesPendingOnTabClose 标签关闭时仍在运行的命令以未知
// 退出码收尾, 长尾命令不丢。
func TestCommandLogFlushesPendingOnTabClose(t *testing.T) {
	sink := &fakeCommandSink{}
	manager, tab, channel, _ := openCommandTestTab(t, sink, "")

	if err := channel.emit([]byte("sleep 100\r\n\x1b]133;B\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.CommandState().Running })
	if err := manager.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.snapshot()) == 1 })
	record := sink.snapshot()[0]
	if record.Command != "sleep 100" || record.ExitCode != nil {
		t.Fatalf("record = %+v, want sleep 100 with unknown exit", record)
	}
}

// TestCommandLogEmptyCommandLineIsNotRecorded 空命令行 (回车即执行) 不产生
// 记录, 避免把纯提示符写进审计。
func TestCommandLogEmptyCommandLineIsNotRecorded(t *testing.T) {
	sink := &fakeCommandSink{}
	_, tab, channel, _ := openCommandTestTab(t, sink, "")

	if err := channel.emit([]byte("\r\n\x1b]133;B\x1b\\\x1b]133;D;0\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.CommandState().Sequence == 1 })
	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("empty command line produced %d records", got)
	}
}

// TestExecLineRecordsExactCommand WinRM 行模式命令带精确文本与退出码落库。
func TestExecLineRecordsExactCommand(t *testing.T) {
	connector := newFakeConnector()
	sink := &fakeCommandSink{}
	manager := NewManager(Config{
		Connector: connector,
		Terminals: newFakeTerminalFactory(),
		Commands:  sink,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := ipc.WithUserID(context.Background(), "u-9")
	connected, err := manager.Connect(ctx, Asset{ID: "asset-win", Kind: KindWinRM})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "win-channel")

	if _, err := manager.ExecLine(context.Background(), tab.ID, "client-a", "ipconfig /all"); err != nil {
		t.Fatal(err)
	}
	records := sink.snapshot()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if record.Command != "ipconfig /all" || record.Source != CommandSourceExec {
		t.Fatalf("record = %+v, want exact exec command", record)
	}
	if record.ExitCode == nil || *record.ExitCode != 0 {
		t.Fatalf("exit code = %v, want 0", record.ExitCode)
	}
	if record.UserID != "u-9" || record.AssetID != "asset-win" || record.TabID != tab.ID || record.SessionID != connected.ID {
		t.Fatalf("attribution = %+v", record)
	}
	if record.FinishedAt.Sub(record.StartedAt) < 0 || time.Since(record.FinishedAt) > time.Minute {
		t.Fatalf("timestamps = %v -> %v", record.StartedAt, record.FinishedAt)
	}
}

// TestCommandLogWrappedCommandJoinsRows 跨行折行的长命令按屏幕行拼接还原。
func TestCommandLogWrappedCommandJoinsRows(t *testing.T) {
	sink := &fakeCommandSink{}
	_, _, channel, _ := openCommandTestTab(t, sink, "")

	long := "echo " + strings.Repeat("a", 88)
	if len(long) < 80 {
		t.Fatalf("test command must exceed 80 columns: %q", long)
	}
	if err := channel.emit([]byte(long + "\r\n\x1b]133;B\x1b\\\x1b]133;D;0\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.snapshot()) == 1 })
	if got := sink.snapshot()[0].Command; got != long {
		t.Fatalf("wrapped command = %q, want %q", got, long)
	}
}
