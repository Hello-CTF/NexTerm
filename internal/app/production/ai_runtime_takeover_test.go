package production

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/takeover"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type takeoverFakeChannel struct {
	generation uint64
	reads      chan []byte
	closed     chan struct{}
	closeOnce  sync.Once

	mu    sync.Mutex
	input []byte
}

func newTakeoverFakeChannel(generation uint64) *takeoverFakeChannel {
	return &takeoverFakeChannel{generation: generation, reads: make(chan []byte, 16), closed: make(chan struct{})}
}

func (c *takeoverFakeChannel) Read(buffer []byte) (int, error) {
	select {
	case data := <-c.reads:
		return copy(buffer, data), nil
	case <-c.closed:
		return 0, io.EOF
	}
}

func (c *takeoverFakeChannel) Write(data []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, base.ErrClosed
	default:
	}
	c.mu.Lock()
	c.input = append(c.input, data...)
	c.mu.Unlock()
	return len(data), nil
}

func (c *takeoverFakeChannel) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *takeoverFakeChannel) CloseWrite() error { return nil }
func (c *takeoverFakeChannel) Stderr() io.Reader { return strings.NewReader("") }
func (c *takeoverFakeChannel) Resize(context.Context, uint32, uint32) error {
	return nil
}
func (c *takeoverFakeChannel) Wait(ctx context.Context) error {
	<-c.closed
	return nil
}
func (c *takeoverFakeChannel) ID() string         { return "takeover-fake" }
func (c *takeoverFakeChannel) Generation() uint64 { return c.generation }

func (c *takeoverFakeChannel) written() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.input...)
}

type takeoverFakeTransport struct {
	mu       sync.Mutex
	channels []*takeoverFakeChannel
}

func (t *takeoverFakeTransport) Kind() string       { return "fake" }
func (t *takeoverFakeTransport) Generation() uint64 { return 1 }
func (t *takeoverFakeTransport) IsAlive() bool      { return true }
func (t *takeoverFakeTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (t *takeoverFakeTransport) Close() error { return nil }
func (t *takeoverFakeTransport) Exec(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{Stdout: "ok"}, nil
}
func (t *takeoverFakeTransport) OpenPTY(_ context.Context, options base.PTYOptions) (base.Channel, error) {
	if options.ExpectedGeneration != 0 && options.ExpectedGeneration != 1 {
		return nil, base.ErrStaleGeneration
	}
	channel := newTakeoverFakeChannel(1)
	t.mu.Lock()
	t.channels = append(t.channels, channel)
	t.mu.Unlock()
	return channel, nil
}

func (t *takeoverFakeTransport) written() []byte {
	t.mu.Lock()
	channels := append([]*takeoverFakeChannel(nil), t.channels...)
	t.mu.Unlock()
	var total []byte
	for _, channel := range channels {
		total = append(total, channel.written()...)
	}
	return total
}

type takeoverScript struct {
	toolCalls atomic.Int32
	release   chan struct{}
	once      sync.Once
}

func (s *takeoverScript) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	var parsed spawnScriptRequest
	if err := json.Unmarshal(body, &parsed); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	if !spawnHasRole(&parsed, "tool") {
		spawnWriteToolCall(writer, "call-keys-1", "send_keys", `{"keys":"ls","enter":true}`)
		return
	}
	if s.toolCalls.Add(1) == 1 {
		s.once.Do(func() { <-s.release })
	}
	spawnWriteToolCall(writer, "call-done-1", "done", `{"summary":"finished","success":true}`)
}

func composeTakeoverRuntime(t *testing.T, database *store.Store, providerURL, assetID string) (*ProductionServices, *takeoverFakeTransport) {
	t.Helper()
	ctx := context.Background()
	profileManager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profileManager.Save(ctx, profiles.Profile{
		BaseURL: providerURL, APIKey: "test-key", Model: "test-model",
		ContextWindow: 32768, Stream: true,
	}); err != nil {
		t.Fatal(err)
	}
	transport := &takeoverFakeTransport{}
	sessions := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return transport, nil
		}),
	})
	connected, err := sessions.Connect(ctx, session.Asset{ID: assetID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	services := &ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions}
	if err := composeAIRuntime(ctx, services); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		services.closeAIRuntime()
		_ = sessions.Close()
	})
	openTakeoverTab(t, services, connected.ID)
	return services, transport
}

func openTakeoverTab(t *testing.T, services *ProductionServices, sessionID string) {
	t.Helper()
	if _, err := services.Sessions.OpenTab(context.Background(), session.OpenTabOptions{
		TabID: "tab-takeover", SessionID: sessionID, ClientID: "client-1", ChannelID: "channel-1", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatal(err)
	}
}

func enterTakeoverWithPrompt(t *testing.T, services *ProductionServices, transport *takeoverFakeTransport) string {
	t.Helper()
	token, err := services.Takeover.Enter(context.Background(), "tab-takeover")
	if err != nil {
		t.Fatal(err)
	}
	transport.mu.Lock()
	channel := transport.channels[len(transport.channels)-1]
	transport.mu.Unlock()
	channel.reads <- []byte("$ ")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		text, err := services.Sessions.ScreenText("tab-takeover")
		if err == nil && strings.Contains(text, "$") {
			return token
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("prompt did not reach the terminal screen")
	return ""
}

func TestProductionTakeoverPauseResumeAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	script := &takeoverScript{release: make(chan struct{})}
	provider := httptest.NewServer(script)
	t.Cleanup(provider.Close)

	firstStore, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := firstStore.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "server"})
	if err != nil {
		t.Fatal(err)
	}
	first, firstTransport := composeTakeoverRuntime(t, firstStore, provider.URL, asset.ID)
	stream := &agent.SliceStream{}
	response, err := first.Takeover.Run(ctx, takeover.RunArgs{TabID: "tab-takeover", Token: enterTakeoverWithPrompt(t, first, firstTransport), Instruction: "list files"}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitOutcomeEvent(t, stream, "confirmRequired")
	if err := first.Takeover.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	result := waitOutcomeEvent(t, stream, "toolResult")
	if !result.OK {
		t.Fatalf("send_keys failed: %+v", result)
	}
	first.Takeover.Pause("tab-takeover")
	close(script.release)
	waitOutcomeEvent(t, stream, "paused")
	if _, found, err := firstStore.CheckpointGet(ctx, "takeover:recovery:"+response.JobID); err != nil || !found {
		t.Fatalf("recovery record missing in sqlite: found=%v err=%v", found, err)
	}
	first.closeAIRuntime()
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	second, secondTransport := composeTakeoverRuntime(t, secondStore, provider.URL, asset.ID)
	token := enterTakeoverWithPrompt(t, second, secondTransport)
	resumed := &agent.SliceStream{}
	if _, err := second.Takeover.Resume(ctx, takeover.ResumeArgs{TabID: "tab-takeover", Token: token, JobID: response.JobID}, agent.StaticStream(resumed)); err != nil {
		t.Fatalf("resume after restart: %v", err)
	}
	events := waitOutcomeClosed(t, resumed)
	done, failed := 0, 0
	for _, event := range events {
		switch event.Type {
		case "done":
			done++
		case "error":
			failed++
		}
	}
	if done != 1 || failed != 0 || events[len(events)-1].Answer != "finished" {
		t.Fatalf("resumed terminal counts done=%d error=%d events=%+v", done, failed, events)
	}
	if got := strings.Count(string(firstTransport.written())+string(secondTransport.written()), "ls\r"); got != 1 {
		t.Fatalf("duplicate write across restart: first=%q second=%q", firstTransport.written(), secondTransport.written())
	}
	if _, found, err := secondStore.CheckpointGet(ctx, "takeover:recovery:"+response.JobID); err != nil || found {
		t.Fatalf("recovery record kept after completion: found=%v err=%v", found, err)
	}
	kind := "takeover"
	rows, err := secondStore.AuditQuery(ctx, store.AuditQuery{Kind: &kind})
	if err != nil || len(rows) == 0 {
		t.Fatalf("takeover audit rows missing: rows=%d err=%v", len(rows), err)
	}
}
