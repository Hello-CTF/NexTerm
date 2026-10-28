package production

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type continuityDurableRecord struct {
	replay  [][]byte
	channel *transcriptFeedChannel
}

type continuityDurableProvider struct {
	mu      sync.Mutex
	records map[string]*continuityDurableRecord
	offsets *delayedTranscriptStore
}

func newContinuityDurableProvider(offsets *delayedTranscriptStore) *continuityDurableProvider {
	return &continuityDurableProvider{records: make(map[string]*continuityDurableRecord), offsets: offsets}
}

func (p *continuityDurableProvider) Create(_ context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	channel := newTranscriptFeedChannel(1)
	p.mu.Lock()
	p.records[options.ID] = &continuityDurableRecord{channel: channel}
	p.mu.Unlock()
	return continuityDurableAttachment{channel}, nil
}

// continuityDurableIdentity is the single incarnation this fake reports; these
// tests exercise transcript continuity, not identity checks.
var continuityDurableIdentity = base.DurableIdentity{Incarnation: "continuity-incarnation"}

func (p *continuityDurableProvider) AttachWithIdentity(ctx context.Context, id string, identity base.DurableIdentity) (base.DurableAttachment, error) {
	return p.AttachFromWithIdentity(ctx, id, identity, 0)
}

func (p *continuityDurableProvider) AttachFromWithIdentity(_ context.Context, id string, identity base.DurableIdentity, _ uint64) (base.DurableAttachment, error) {
	if identity.Incarnation != continuityDurableIdentity.Incarnation {
		return nil, durable.ErrIdentity
	}
	p.mu.Lock()
	record := p.records[id]
	if record == nil {
		p.mu.Unlock()
		return nil, errors.New("no such durable session")
	}
	channel := newTranscriptFeedChannel(1)
	record.channel = channel
	replay := append([][]byte(nil), record.replay...)
	p.mu.Unlock()
	for _, data := range replay {
		channel.reads <- bytes.Clone(data)
	}
	return continuityDurableAttachment{channel}, nil
}

func (p *continuityDurableProvider) AttachTailWithIdentity(ctx context.Context, id string, identity base.DurableIdentity, _ base.DurableTailOptions) (base.DurableAttachment, error) {
	return p.AttachWithIdentity(ctx, id, identity)
}

func (p *continuityDurableProvider) ListDurableDescriptors(context.Context) ([]base.DurableDescriptor, error) {
	return nil, base.ErrUnsupported
}

func (p *continuityDurableProvider) RenameDurable(context.Context, base.DurableRenameOptions) (base.DurableRenameResult, error) {
	return base.DurableRenameResult{}, base.ErrUnsupported
}

func (p *continuityDurableProvider) KillWithIdentity(context.Context, string, base.DurableIdentity) error {
	return base.ErrUnsupported
}

func (p *continuityDurableProvider) ReadDurableHistory(context.Context, string, base.DurableIdentity, base.DurableHistoryRequest) (base.DurableHistoryChunk, error) {
	return base.DurableHistoryChunk{}, base.ErrUnsupported
}

func (p *continuityDurableProvider) DurableTranscriptCatchUpBytesContext(_ context.Context, tabID string) int64 {
	_, _, offsets, _ := p.offsets.snapshot()
	return offsets[tabID]
}

func (p *continuityDurableProvider) PersistDurableTranscriptOffsetContext(_ context.Context, tabID string, offset int64) {
	p.offsets.mu.Lock()
	p.offsets.durableOffsets[tabID] = offset
	p.offsets.mu.Unlock()
}

func (p *continuityDurableProvider) DeleteDurableTranscriptOffsetContext(ctx context.Context, tabID string) {
	p.PersistDurableTranscriptOffsetContext(ctx, tabID, 0)
}

func (p *continuityDurableProvider) channel(tabID string) *transcriptFeedChannel {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.records[tabID].channel
}

func (p *continuityDurableProvider) setReplay(tabID string, replay ...[]byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	record := p.records[tabID]
	record.replay = nil
	for _, data := range replay {
		record.replay = append(record.replay, bytes.Clone(data))
	}
}

type continuityDurableAttachment struct{ *transcriptFeedChannel }

func (a continuityDurableAttachment) Kill(context.Context) error { return nil }

func startContinuityManager(t *testing.T, database *delayedTranscriptStore, provider *continuityDurableProvider) (*session.Manager, *transcriptWriter) {
	t.Helper()
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger()})
	cleanupDelayedTranscriptWriter(t, writer, database)
	if err := writer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
			return newTranscriptFeedTransport(asset.Kind, generation), nil
		}),
		Durable: provider, TranscriptOffsets: provider, Transcripts: writer, Logger: discardTranscriptLogger(),
		OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	return manager, writer
}

// A gap reported by the session advances the durable offset by the dropped
// bytes and records a visible notice instead of them.
func TestTranscriptWriterDurableGapAdvancesOffset(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	database.unblock()
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger()})
	cleanupDelayedTranscriptWriter(t, writer, database)
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	writer.SessionStarted(ctx, session.TranscriptInfo{SessionID: "gap-session", AssetID: "gap-asset", AssetName: "gap", AssetKind: session.KindSSH})
	writer.SessionOutput(ctx, "gap-durable", "gap-session", "gap-tab", []byte("0123456789"))
	writer.SessionOutputGap(ctx, "gap-durable", "gap-session", "gap-tab", 1000)
	writer.SessionOutput(ctx, "gap-durable", "gap-session", "gap-tab", []byte("tail"))
	writer.SessionEnded(ctx, "gap-session")
	if err := writer.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	chunks, _, offsets, _ := database.snapshot()
	if offsets["gap-durable"] != 1014 {
		t.Fatalf("durable offset = %d, want 1014", offsets["gap-durable"])
	}
	var recorded []byte
	for _, chunk := range chunks {
		recorded = append(recorded, chunk.Data...)
	}
	if !bytes.Contains(recorded, []byte("1000 byte(s) of output were dropped")) || !bytes.HasSuffix(recorded, []byte("tail")) {
		t.Fatalf("transcript = %q", recorded)
	}
}

func TestTranscriptWriterDurableRecoveryDoesNotSkipOrDuplicate(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	database.failDurableAppends = 1
	database.unblock()
	provider := newContinuityDurableProvider(database)
	first, firstWriter := startContinuityManager(t, database, provider)
	connected, err := first.Connect(ctx, session.Asset{ID: "asset-continuity", Name: "local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := first.OpenTab(ctx, session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "continuity-a", Cols: 80, Rows: 24,
		Durable: &session.DurableTabOptions{Command: []string{"/bin/sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := bytes.Repeat([]byte("a"), 100)
	b := []byte("b")
	if err := provider.channel(opened.ID).send(a); err != nil {
		t.Fatal(err)
	}
	select {
	case <-database.appendFailed:
	case <-time.After(5 * time.Second):
		t.Fatal("first durable append did not fail")
	}
	if err := provider.channel(opened.ID).send(b); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		started := database.startedRows()
		if len(started) == 1 {
			chunks := database.chunksForTranscript(started[0].ID)
			if len(chunks) > 0 && bytes.Equal(chunks[len(chunks)-1].Data, b) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("later successful durable block was not persisted")
		}
		time.Sleep(time.Millisecond)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := firstWriter.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, offsets, _ := database.snapshot()
	if offsets[opened.ID] != 0 {
		t.Fatalf("offset before recovery crossed the failed block: %d", offsets[opened.ID])
	}
	provider.setReplay(opened.ID, a, b)

	second, secondWriter := startContinuityManager(t, database, provider)
	recovered, err := second.Connect(ctx, session.Asset{ID: "asset-continuity", Name: "local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.OpenTab(ctx, session.OpenTabOptions{
		TabID: opened.ID, SessionID: recovered.ID, ClientID: "client-b", ChannelID: "continuity-b", Cols: 80, Rows: 24,
		Durable: &session.DurableTabOptions{Recover: true, Identity: &continuityDurableIdentity},
	}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		_, _, offsets, _ = database.snapshot()
		if offsets[opened.ID] == int64(len(a)+len(b)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("continuous replay did not persist: offset=%d", offsets[opened.ID])
		}
		time.Sleep(time.Millisecond)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := secondWriter.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	started := database.startedRows()
	if len(started) != 2 {
		t.Fatalf("recovery periods = %+v", started)
	}
	var replayed [][]byte
	for _, chunk := range database.chunksForTranscript(started[1].ID) {
		if chunk.Kind == 0 {
			replayed = append(replayed, chunk.Data)
		}
	}
	if len(replayed) != 2 || !bytes.Equal(replayed[0], a) || !bytes.Equal(replayed[1], b) {
		t.Fatalf("recovery skipped or duplicated replay data: %+v", replayed)
	}
}

func TestTranscriptWriterDurableRecoveryMultipleIDsStayIndependent(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	database.failDurableAppends = 1
	database.unblock()
	provider := newContinuityDurableProvider(database)
	first, firstWriter := startContinuityManager(t, database, provider)
	connected, err := first.Connect(ctx, session.Asset{ID: "asset-multi-continuity", Name: "local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	openTab := func(manager *session.Manager, sessionID, channelID, tabID string, recover bool) session.TabInfo {
		t.Helper()
		options := session.OpenTabOptions{
			TabID: tabID, SessionID: sessionID, ClientID: "client-a", ChannelID: channelID, Cols: 80, Rows: 24,
		}
		if recover {
			options.Durable = &session.DurableTabOptions{Recover: true, Identity: &continuityDurableIdentity}
		} else {
			options.Durable = &session.DurableTabOptions{Command: []string{"/bin/sh"}}
		}
		info, err := manager.OpenTab(ctx, options)
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	tabA := openTab(first, connected.ID, "multi-a", "", false)
	tabB := openTab(first, connected.ID, "multi-b", "", false)
	a1, a2 := []byte("aa"), []byte("z")
	b1, b2 := []byte("b"), []byte("y")
	if err := provider.channel(tabA.ID).send(a1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-database.appendFailed:
	case <-time.After(5 * time.Second):
		t.Fatal("first durable append did not fail")
	}
	for _, step := range []struct {
		tabID  string
		output []byte
	}{{tabB.ID, b1}, {tabA.ID, a2}, {tabB.ID, b2}} {
		if err := provider.channel(step.tabID).send(step.output); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, offsets, _ := database.snapshot()
		if offsets[tabB.ID] == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("independent durable ID did not advance: %+v", offsets)
		}
		time.Sleep(time.Millisecond)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := firstWriter.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, offsets, _ := database.snapshot()
	if offsets[tabA.ID] != 0 || offsets[tabB.ID] != 2 {
		t.Fatalf("offsets before multi-ID recovery: %+v", offsets)
	}
	provider.setReplay(tabA.ID, a1, a2)
	provider.setReplay(tabB.ID, b1, b2)

	second, secondWriter := startContinuityManager(t, database, provider)
	recovered, err := second.Connect(ctx, session.Asset{ID: "asset-multi-continuity", Name: "local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	recoveredA := openTab(second, recovered.ID, "multi-recovered-a", tabA.ID, true)
	recoveredB := openTab(second, recovered.ID, "multi-recovered-b", tabB.ID, true)
	deadline = time.Now().Add(5 * time.Second)
	for {
		_, _, offsets, _ = database.snapshot()
		tab, err := second.Tab(recoveredB.ID)
		if err == nil && offsets[recoveredA.ID] == 3 && offsets[recoveredB.ID] == 2 && tab.Info().LastOutputAt != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("multi-ID replay did not complete: offsets=%+v err=%v", offsets, err)
		}
		time.Sleep(time.Millisecond)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := secondWriter.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	started := database.startedRows()
	if len(started) != 2 {
		t.Fatalf("multi-ID recovery periods = %+v", started)
	}
	var replayedA, replayedB [][]byte
	for _, chunk := range database.chunksForTranscript(started[1].ID) {
		if chunk.Kind != 0 {
			continue
		}
		switch chunk.TabID {
		case tabA.ID:
			replayedA = append(replayedA, chunk.Data)
		case tabB.ID:
			replayedB = append(replayedB, chunk.Data)
		}
	}
	if len(replayedA) != 2 || !bytes.Equal(replayedA[0], a1) || !bytes.Equal(replayedA[1], a2) {
		t.Fatalf("failed durable ID replay was skipped or duplicated: %+v", replayedA)
	}
	if len(replayedB) != 0 {
		t.Fatalf("continuous durable ID replay was recorded again: %+v", replayedB)
	}
}

var _ session.OwnerScopedDurableTranscriptOffsetStore = (*continuityDurableProvider)(nil)
