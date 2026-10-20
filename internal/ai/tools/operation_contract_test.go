package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

// Every initiated mutation reports the same deterministic operation ID on
// success and failure; connector failures keep the residual session; only an
// explicit not-connected verdict may trigger the connect stage.

// failSecondWriteTerminal lets the paste land and fails the Enter write.
type failSecondWriteTerminal struct {
	fakeTerminal
	writes int
}

func (f *failSecondWriteTerminal) Write(ctx context.Context, tabID string, data []byte) error {
	f.writes++
	if f.writes == 2 {
		return errors.New("pty closed")
	}
	return f.fakeTerminal.Write(ctx, tabID, data)
}

func (f *failSecondWriteTerminal) WritePaste(ctx context.Context, tabID, text string) error {
	return f.Write(ctx, tabID, terminal.EncodePaste(text, false))
}

func TestSendKeysPasteEnterFailureCarriesOperationID(t *testing.T) {
	terminal := &failSecondWriteTerminal{fakeTerminal: fakeTerminal{screen: Screen{Text: "$ ", Cols: 80, Rows: 24}}}
	registry := p1Registry(terminal)
	result := registry.Execute(context.Background(), "job", Scope{TabID: "t"},
		p1Call("k", "send_keys", `{"keys":"ls","paste":true,"enter":true}`), nil)
	if result.OK {
		t.Fatalf("failed Enter reported success: %+v", result)
	}
	if result.OperationID == "" {
		t.Fatalf("partial paste failure lost the operation ID: %+v", result)
	}
	if !strings.Contains(result.Text, "不要盲重发") || !strings.Contains(result.Text, "pty closed") {
		t.Fatalf("partial paste failure contract = %q", result.Text)
	}
	// The same operation referenced again yields the same ID.
	again := registry.Execute(context.Background(), "job", Scope{TabID: "t"},
		p1Call("k", "send_keys", `{"keys":"ls","paste":true,"enter":true}`), nil)
	if again.OperationID != result.OperationID {
		t.Fatalf("operation ID not deterministic: %q vs %q", again.OperationID, result.OperationID)
	}
}

type residualConnector struct {
	sessionID string
	err       error
	calls     int
}

func (c *residualConnector) ConnectAsset(context.Context, string) (string, error) {
	c.calls++
	return c.sessionID, c.err
}

func TestTerminalCreateConnectErrorKeepsResidualSession(t *testing.T) {
	registry := p1Registry(newTabFake(fakeTab("t1", "s1", "$ ", 0)))
	registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) {
		return "", fmt.Errorf("资产未连接: %w", ErrNoLiveSession)
	}
	registry.deps.AssetConnector = &residualConnector{sessionID: "s-residual", err: errors.New("dial failed")}
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`), nil)
	if result.OK {
		t.Fatalf("connect failure reported success: %+v", result)
	}
	if !strings.Contains(result.Text, "s-residual") || !strings.Contains(result.Text, "dial failed") {
		t.Fatalf("residual session dropped: %q", result.Text)
	}
	if !strings.Contains(result.Text, "残留") {
		t.Fatalf("residual session not named as residual: %q", result.Text)
	}
	if result.OperationID == "" {
		t.Fatalf("connect failure lost the operation ID: %+v", result)
	}
}

func TestTerminalCreateConnectFailureAfterInitiatedConnect(t *testing.T) {
	registry := p1Registry(newTabFake(fakeTab("t1", "s1", "$ ", 0)))
	registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) {
		return "", fmt.Errorf("资产未连接: %w", ErrNoLiveSession)
	}
	registry.deps.AssetConnector = &residualConnector{err: errors.New("dial failed")}
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`), nil)
	if result.OK || !strings.Contains(result.Text, "dial failed") {
		t.Fatalf("connect failure = %+v", result)
	}
	if result.OperationID == "" {
		t.Fatalf("initiated connect failure lost the operation ID: %+v", result)
	}
}

func TestTerminalCreateOnlyNotConnectedTriggersConnect(t *testing.T) {
	for name, resolveErr := range map[string]error{
		"forbidden":   session.ErrForbidden,
		"unsupported": session.ErrUnsupported,
		"transient":   errors.New("backend unavailable"),
	} {
		t.Run(name, func(t *testing.T) {
			connector := &residualConnector{sessionID: "s9"}
			registry := p1Registry(newTabFake(fakeTab("t1", "s1", "$ ", 0)))
			registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) {
				return "", resolveErr
			}
			registry.deps.AssetConnector = connector
			result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
				p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`), nil)
			if result.OK {
				t.Fatalf("resolve error %v reported success", resolveErr)
			}
			if connector.calls != 0 {
				t.Fatalf("connect attempted on %v", resolveErr)
			}
			if !strings.Contains(result.Text, resolveErr.Error()) {
				t.Fatalf("resolver error masked: %q", result.Text)
			}
		})
	}

	t.Run("not connected connects", func(t *testing.T) {
		connector := &residualConnector{sessionID: "s9"}
		registry := p1Registry(newTabFake(fakeTab("t1", "s1", "$ ", 0)))
		registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) {
			return "", fmt.Errorf("资产未连接: %w", ErrNoLiveSession)
		}
		registry.deps.AssetConnector = connector
		result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
			p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`), nil)
		if !result.OK || connector.calls != 1 {
			t.Fatalf("not-connected did not connect: %+v calls=%d", result, connector.calls)
		}
		if result.OperationID == "" {
			t.Fatalf("connected create lost the operation ID: %+v", result)
		}
	})
}
