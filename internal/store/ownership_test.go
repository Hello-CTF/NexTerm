package store

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestUserOwnershipIsolation(t *testing.T) {
	database := testStore(t)
	ctxA := ipc.WithUserID(context.Background(), "user-a")
	ctxB := ipc.WithUserID(context.Background(), "user-b")

	groupA, err := database.GroupCreate(ctxA, GroupInput{Name: "group-a"})
	if err != nil {
		t.Fatal(err)
	}
	groupB, err := database.GroupCreate(ctxB, GroupInput{Name: "group-b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.GroupGet(ctxB, groupA.ID); err == nil {
		t.Fatal("user B read user A's group")
	}
	groupsA, err := database.GroupList(ctxA)
	if err != nil || len(groupsA) != 1 || groupsA[0].ID != groupA.ID {
		t.Fatalf("user A groups = %+v, err=%v", groupsA, err)
	}
	groupsB, err := database.GroupList(ctxB)
	if err != nil || len(groupsB) != 1 || groupsB[0].ID != groupB.ID {
		t.Fatalf("user B groups = %+v, err=%v", groupsB, err)
	}

	credentialA, err := database.CredentialPut(ctxA, CredentialInput{Name: "credential-a", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}})
	if err != nil {
		t.Fatal(err)
	}
	credentialB, err := database.CredentialPut(ctxB, CredentialInput{Name: "credential-b", Kind: "password", Nonce: []byte{3}, Blob: []byte{4}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CredentialGetRow(ctxB, credentialA); err == nil {
		t.Fatal("user B read user A's credential")
	}
	credentialsA, err := database.CredentialList(ctxA)
	if err != nil || len(credentialsA) != 1 || credentialsA[0].ID != credentialA {
		t.Fatalf("user A credentials = %+v, err=%v", credentialsA, err)
	}
	credentialsB, err := database.CredentialList(ctxB)
	if err != nil || len(credentialsB) != 1 || credentialsB[0].ID != credentialB {
		t.Fatalf("user B credentials = %+v, err=%v", credentialsB, err)
	}

	assetA, err := database.AssetCreate(ctxA, AssetInput{GroupID: &groupA.ID, Kind: "ssh", Name: "asset-a", CredID: &credentialA, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	assetB, err := database.AssetCreate(ctxB, AssetInput{GroupID: &groupB.ID, Kind: "ssh", Name: "asset-b", CredID: &credentialB, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.AssetGet(ctxB, assetA.ID); err == nil {
		t.Fatal("user B read user A's asset")
	}
	assetsA, err := database.AssetList(ctxA, false)
	if err != nil || len(assetsA) != 1 || assetsA[0].ID != assetA.ID {
		t.Fatalf("user A assets = %+v, err=%v", assetsA, err)
	}
	assetsB, err := database.AssetList(ctxB, false)
	if err != nil || len(assetsB) != 1 || assetsB[0].ID != assetB.ID {
		t.Fatalf("user B assets = %+v, err=%v", assetsB, err)
	}
	if matches, err := database.AssetSearch(ctxB, "asset-a"); err != nil || len(matches) != 0 {
		t.Fatalf("user B search user A's asset = %+v, err=%v", matches, err)
	}
	if usage, err := database.CredentialUsage(ctxB, credentialA); err != nil || len(usage) != 0 {
		t.Fatalf("user B credential usage = %+v, err=%v", usage, err)
	}
	if err := database.AssetDelete(ctxB, assetA.ID); err == nil {
		t.Fatal("user B deleted user A's asset")
	}
	if _, err := database.AssetGet(ctxA, assetA.ID); err != nil {
		t.Fatalf("user A's asset changed after cross-user delete: %v", err)
	}

	snippetA, err := database.SnippetCreate(ctxA, "snippet-a", "echo a", &groupA.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	snippetB, err := database.SnippetCreate(ctxB, "snippet-b", "echo b", &groupB.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.SnippetGet(ctxB, snippetA.ID); err == nil {
		t.Fatal("user B read user A's snippet")
	}
	snippetsA, err := database.SnippetList(ctxA)
	if err != nil || len(snippetsA) != 1 || snippetsA[0].ID != snippetA.ID {
		t.Fatalf("user A snippets = %+v, err=%v", snippetsA, err)
	}
	snippetsB, err := database.SnippetList(ctxB)
	if err != nil || len(snippetsB) != 1 || snippetsB[0].ID != snippetB.ID {
		t.Fatalf("user B snippets = %+v, err=%v", snippetsB, err)
	}

	if err := database.KnownHostAccept(ctxA, "shared.example.com", 22, "ssh-ed25519", "SHA256:a"); err != nil {
		t.Fatal(err)
	}
	if err := database.KnownHostAccept(ctxB, "shared.example.com", 22, "ssh-ed25519", "SHA256:b"); err != nil {
		t.Fatal(err)
	}
	knownA, foundA, err := database.KnownHostGet(ctxA, "shared.example.com", 22, "ssh-ed25519")
	if err != nil || !foundA || knownA.Fingerprint != "SHA256:a" {
		t.Fatalf("user A known host = %+v, found=%v, err=%v", knownA, foundA, err)
	}
	knownB, foundB, err := database.KnownHostGet(ctxB, "shared.example.com", 22, "ssh-ed25519")
	if err != nil || !foundB || knownB.Fingerprint != "SHA256:b" {
		t.Fatalf("user B known host = %+v, found=%v, err=%v", knownB, foundB, err)
	}
	if err := database.KnownHostRemove(ctxB, knownA.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := database.KnownHostGet(ctxA, "shared.example.com", 22, "ssh-ed25519"); err != nil || !found {
		t.Fatalf("user A known host removed by user B: found=%v, err=%v", found, err)
	}

	conversationA, err := database.ConvCreate(ctxA, "conversation-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	conversationB, err := database.ConvCreate(ctxB, "conversation-b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MsgInsert(ctxA, conversationA.ID, "user", "message-a", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := database.MsgInsert(ctxB, conversationB.ID, "user", "message-b", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ConvGet(ctxB, conversationA.ID); err == nil {
		t.Fatal("user B read user A's conversation")
	}
	messagesA, err := database.MsgList(ctxA, conversationA.ID)
	if err != nil || len(messagesA) != 1 {
		t.Fatalf("user A messages = %+v, err=%v", messagesA, err)
	}
	messagesB, err := database.MsgList(ctxB, conversationB.ID)
	if err != nil || len(messagesB) != 1 {
		t.Fatalf("user B messages = %+v, err=%v", messagesB, err)
	}
	if crossMessages, err := database.MsgList(ctxB, conversationA.ID); err != nil || len(crossMessages) != 0 {
		t.Fatalf("user B read user A's messages = %+v, err=%v", crossMessages, err)
	}
	if err := database.ConvDelete(ctxB, conversationA.ID); err == nil {
		t.Fatal("user B deleted user A's conversation")
	}

	if err := database.RunInsert(ctxA, RunRow{ID: "run-a", ConversationID: conversationA.ID, Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := database.RunInsert(ctxB, RunRow{ID: "run-b", ConversationID: conversationB.ID, Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.RunGet(ctxB, "run-a"); err == nil {
		t.Fatal("user B read user A's run")
	}
	if err := database.RunDelete(ctxB, "run-a"); err == nil {
		t.Fatal("user B deleted user A's run")
	}

	if err := database.TranscriptStart(ctxA, TranscriptRow{ID: "transcript-a", SessionID: "session-a", AssetID: assetA.ID, StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := database.TranscriptStart(ctxB, TranscriptRow{ID: "transcript-b", SessionID: "session-b", AssetID: assetB.ID, StartedAt: 2}); err != nil {
		t.Fatal(err)
	}
	if err := database.TranscriptAppendChunks(ctxA, "transcript-a", []TranscriptChunkRow{{Seq: 0, TabID: "tab-a", TS: 1, Data: []byte("a")}}); err != nil {
		t.Fatal(err)
	}
	if err := database.TranscriptAppendChunks(ctxB, "transcript-b", []TranscriptChunkRow{{Seq: 0, TabID: "tab-b", TS: 2, Data: []byte("b")}}); err != nil {
		t.Fatal(err)
	}
	transcriptsA, err := database.TranscriptListByAsset(ctxA, assetA.ID, 10)
	if err != nil || len(transcriptsA) != 1 || transcriptsA[0].ID != "transcript-a" {
		t.Fatalf("user A transcripts = %+v, err=%v", transcriptsA, err)
	}
	transcriptsB, err := database.TranscriptListByAsset(ctxB, assetB.ID, 10)
	if err != nil || len(transcriptsB) != 1 || transcriptsB[0].ID != "transcript-b" {
		t.Fatalf("user B transcripts = %+v, err=%v", transcriptsB, err)
	}
	if chunks, err := database.TranscriptChunks(ctxB, "transcript-a", 0, 1024); err != nil || len(chunks) != 0 {
		t.Fatalf("user B read user A's transcript chunks = %+v, err=%v", chunks, err)
	}
	if err := database.TranscriptDelete(ctxB, "transcript-a"); err == nil {
		t.Fatal("user B deleted user A's transcript")
	}

	recordingA, err := database.RecordingStart(ctxA, "session-a", "tab-a", "/tmp/a.recording")
	if err != nil {
		t.Fatal(err)
	}
	recordingB, err := database.RecordingStart(ctxB, "session-b", "tab-b", "/tmp/b.recording")
	if err != nil {
		t.Fatal(err)
	}
	recordingsA, err := database.RecordingList(ctxA)
	if err != nil || len(recordingsA) != 1 || recordingsA[0].ID != recordingA {
		t.Fatalf("user A recordings = %+v, err=%v", recordingsA, err)
	}
	recordingsB, err := database.RecordingList(ctxB)
	if err != nil || len(recordingsB) != 1 || recordingsB[0].ID != recordingB {
		t.Fatalf("user B recordings = %+v, err=%v", recordingsB, err)
	}
	if err := database.RecordingEnd(ctxB, recordingA); err != nil {
		t.Fatal(err)
	}
	recordingsA, err = database.RecordingList(ctxA)
	if err != nil || len(recordingsA) != 1 || recordingsA[0].EndedAt != nil {
		t.Fatalf("user A recording changed by user B = %+v, err=%v", recordingsA, err)
	}
}
