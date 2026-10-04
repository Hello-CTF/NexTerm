//go:build darwin || linux

package production

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
)

type controlEventLog struct {
	mu     sync.Mutex
	events []session.ControlEvent
}

func (l *controlEventLog) Emit(_ context.Context, event ipc.Event) error {
	if event.Event != ipc.Topic(session.TopicTerminalControl) {
		return nil
	}
	payload, ok := event.Payload.(session.ControlEvent)
	if !ok {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, payload)
	return nil
}

func (l *controlEventLog) forTab(tabID string) []session.ControlEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	events := make([]session.ControlEvent, 0, len(l.events))
	for _, event := range l.events {
		if event.TabID == tabID {
			events = append(events, event)
		}
	}
	return events
}

func (l *controlEventLog) maxVersion(tabID string) uint64 {
	var max uint64
	for _, event := range l.forTab(tabID) {
		if event.Version > max {
			max = event.Version
		}
	}
	return max
}

func TestProductionDurableRecoveryResumesVersionsAndGridWithoutEcho(t *testing.T) {
	requireRealTmux(t)
	dataDir := durableTestDataDir(t)

	firstLog := &controlEventLog{}
	firstFactory := &bridgeTestFactory{}
	first := newRecoveryTestProduction(t, dataDir, firstFactory, firstLog)
	connectedResponse := dispatchDurableTest(t, first, "session_connect_local", `null`, "", "")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &connected)
	channelID := "durable-recovery-channel"
	attachResponse := dispatchDurableTest(t, first, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelID, "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	waitForProductionOutput(t, firstFactory.at(channelID, 0), "$ ")
	requireProductionNull(t, dispatchDurableTest(t, first, "terminal_resize", `{"tabId":"`+tabID+`","cols":100,"rows":30}`, "", "client-a"))

	before := firstLog.forTab(tabID)
	if len(before) < 2 {
		t.Fatalf("control events before restart = %+v", before)
	}
	lastBefore := before[len(before)-1]
	if lastBefore.GridRevision == 0 || lastBefore.Cols != 100 || lastBefore.Rows != 30 {
		t.Fatalf("last pre-restart event = %+v, want resized 100x30 with a bumped revision", lastBefore)
	}
	highWater := firstLog.maxVersion(tabID)
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	floorEvent, floorGrid := readDurableVersionsFloor(t, filepath.Join(dataDir, "durable", "state", tabID, "versions"))
	if floorEvent < highWater || floorGrid != lastBefore.GridRevision {
		t.Fatalf("persisted floor = %d/%d, want event >= %d and grid %d", floorEvent, floorGrid, highWater, lastBefore.GridRevision)
	}

	secondLog := &controlEventLog{}
	secondFactory := &bridgeTestFactory{}
	second := newRecoveryTestProduction(t, dataDir, secondFactory, secondLog)
	connectedResponse = dispatchDurableTest(t, second, "session_connect_local", `null`, "", "")
	requireStoreTestResponse(t, connectedResponse, &connected)
	recoveredResponse := dispatchDurableTest(t, second, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":65536}`, channelID, "client-a")
	var recovered attachedTabDTO
	requireStoreTestResponse(t, recoveredResponse, &recovered)
	if recovered.Cols != 100 || recovered.Rows != 30 {
		t.Fatalf("recovered tab grid = %dx%d, want the real 100x30 window", recovered.Cols, recovered.Rows)
	}

	after := secondLog.forTab(tabID)
	if len(after) == 0 {
		t.Fatal("recovery emitted no control event")
	}
	firstAfter := after[0]
	if firstAfter.Version <= highWater {
		t.Fatalf("first recovered event version = %d, want > high-water %d", firstAfter.Version, highWater)
	}
	if firstAfter.GridRevision != lastBefore.GridRevision || firstAfter.Cols != lastBefore.Cols || firstAfter.Rows != lastBefore.Rows {
		t.Fatalf("recovery event = %+v, want unchanged grid %+v", firstAfter, lastBefore)
	}
	for _, event := range after {
		if event.Version <= highWater {
			t.Fatalf("post-recovery event version = %d, want > high-water %d", event.Version, highWater)
		}
	}

	requireProductionNull(t, dispatchDurableTest(t, second, "terminal_resize", `{"tabId":"`+tabID+`","cols":110,"rows":40}`, "", "client-a"))
	events := secondLog.forTab(tabID)
	resized := events[len(events)-1]
	if resized.GridRevision != lastBefore.GridRevision+1 {
		t.Fatalf("post-recovery revision = %d, want %d", resized.GridRevision, lastBefore.GridRevision+1)
	}
	if resized.Version <= firstAfter.Version {
		t.Fatalf("post-recovery resize version = %d, want > %d", resized.Version, firstAfter.Version)
	}
	waitForProductionOutput(t, secondFactory.at(channelID, 0), "$ ")

	requireProductionNull(t, dispatchDurableTest(t, second, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-a"}`, "", "client-a"))
	if err := second.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "durable", "state", tabID, "versions")); !os.IsNotExist(err) {
		t.Fatalf("killed durable identity kept its version floor: %v", err)
	}
}

func newRecoveryTestProduction(t *testing.T, dataDir string, factory *bridgeTestFactory, events ipc.Emitter) *Production {
	t.Helper()
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
			Events:  events,
		},
		DataDir: dataDir, Desktop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	return production
}

func readDurableVersionsFloor(t *testing.T, path string) (eventVersion, gridRevision uint64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read version floor: %v", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		t.Fatalf("version floor %q has %d fields, want 2", data, len(fields))
	}
	eventVersion, err = strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	gridRevision, err = strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return eventVersion, gridRevision
}
