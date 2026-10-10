package sync

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

func TestEngineOperationsSerializeSyncApplyAndShutdown(t *testing.T) {
	instance := newTestInstance(t, true)
	server := newTestSyncServer(t)
	ctx := context.Background()
	const password = "alice-password"
	dek := server.createUser(t, "alice", password)

	transcriptID := ids.New()
	seed := newTestDevice(t)
	startedAt, endedAt := int64(100), int64(200)
	putDeviceTranscript(t, seed, transcriptID, startedAt, &endedAt, []byte("data"), true)
	syncDevice(t, seed, server, "alice", password)
	transcriptRow, err := seed.db.TranscriptGet(ctx, transcriptID)
	if err != nil {
		t.Fatal(err)
	}
	applyTranscript := ApplyObject{ID: transcriptID, Kind: KindTranscript, Payload: applyPayload(t, transcriptObject{
		ID: transcriptID, SessionID: transcriptRow.SessionID, AssetID: transcriptRow.AssetID,
		AssetName: transcriptRow.AssetName, AssetKind: transcriptRow.AssetKind,
		StartedAt: startedAt, EndedAt: endedAt, Bytes: 4, Chunks: 1,
		Content: []transcriptChunkObject{{Seq: 0, TabID: "tab-1", TS: startedAt, Data: []byte("data")}},
	})}
	groupID := ids.New()
	putTestGroup(t, instance, groupID, nil, "group")

	idsReached := make(chan struct{})
	releaseSync := make(chan struct{})
	server.afterIDs = func() {
		close(idsReached)
		<-releaseSync
	}
	defer func() {
		select {
		case <-releaseSync:
		default:
			close(releaseSync)
		}
	}()
	syncDone := make(chan error, 1)
	go func() {
		_, err := instance.service.engine.Sync(ctx, RemoteConfig{URL: server.URL, Username: "alice", Password: password})
		syncDone <- err
	}()
	<-idsReached

	applyStarted := make(chan struct{})
	applyDone := make(chan error, 1)
	go func() {
		close(applyStarted)
		_, err := instance.service.ApplyObjects(ctx, ApplyObjectsRequest{Objects: []ApplyObject{applyTranscript}})
		applyDone <- err
	}()
	shutdownStarted := make(chan struct{})
	shutdownDone := make(chan error, 1)
	go func() {
		close(shutdownStarted)
		shutdownDone <- instance.service.Shutdown(ctx)
	}()
	<-applyStarted
	<-shutdownStarted
	select {
	case err := <-applyDone:
		t.Fatalf("ApplyObjects completed while Sync was paused: %v", err)
	case err := <-shutdownDone:
		t.Fatalf("Shutdown completed while Sync was paused: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseSync)
	if err := <-syncDone; err != nil {
		t.Fatalf("Sync failed after controlled interleaving: %v", err)
	}
	if err := <-applyDone; err != nil {
		t.Fatalf("ApplyObjects failed after controlled interleaving: %v", err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown failed after controlled interleaving: %v", err)
	}

	var blob []byte
	if err := server.db.DB().QueryRowContext(ctx, "SELECT blob FROM user_sync_object WHERE id = ?", groupID).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if _, err := openObject(dek, blob, groupID, KindGroup); err != nil {
		t.Fatalf("Shutdown corrupted the DEK used by Sync: %v", err)
	}
	chunks, err := instance.db.TranscriptChunks(ctx, transcriptID, 0, 1<<20)
	if err != nil || len(chunks) != 1 || string(chunks[0].Data) != "data" {
		t.Fatalf("transcript chunks after controlled interleaving = %+v, err=%v", chunks, err)
	}
}
