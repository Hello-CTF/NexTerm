package production

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/takeover"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// TestProductionTakeoverResumeFailsClosedWhenRealAssetChanged pins the
// production TabAsset binding: a recovery record is bound to the real target
// asset the tab resolved at run time, and resuming after the tab's real
// asset changed fails closed with a mismatch — the record stays for the
// legitimate asset and nothing executes on the wrong target.
func TestProductionTakeoverResumeFailsClosedWhenRealAssetChanged(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	script := &takeoverScript{release: make(chan struct{})}
	provider := httptest.NewServer(script)
	t.Cleanup(provider.Close)

	firstStore, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	assetOne, err := firstStore.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "server-one"})
	if err != nil {
		t.Fatal(err)
	}
	first, firstTransport := composeTakeoverRuntime(t, firstStore, provider.URL, assetOne.ID)
	stream := &agent.SliceStream{}
	response, err := first.Takeover.Run(ctx, takeover.RunArgs{TabID: "tab-takeover", Token: enterTakeoverWithPrompt(t, first, firstTransport), Instruction: "list files"}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitOutcomeEvent(t, stream, "confirmRequired")
	if err := first.Takeover.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	waitOutcomeEvent(t, stream, "toolResult")
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

	// The tab id is reused on a session whose real asset differs: resume must
	// fail closed against the recorded asset evidence, not execute on the
	// wrong target, and keep the record for the legitimate asset.
	secondStore, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	assetTwo, err := secondStore.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "server-two"})
	if err != nil {
		t.Fatal(err)
	}
	second, secondTransport := composeTakeoverRuntime(t, secondStore, provider.URL, assetTwo.ID)
	token := enterTakeoverWithPrompt(t, second, secondTransport)
	resumed := &agent.SliceStream{}
	_, err = second.Takeover.Resume(ctx, takeover.ResumeArgs{TabID: "tab-takeover", Token: token, JobID: response.JobID}, agent.StaticStream(resumed))
	if !errors.Is(err, takeover.ErrResumeMismatch) {
		t.Fatalf("resume on a changed real asset = %v, want mismatch", err)
	}
	if _, found, err := secondStore.CheckpointGet(ctx, "takeover:recovery:"+response.JobID); err != nil || !found {
		t.Fatalf("failed resume must keep the recovery record: found=%v err=%v", found, err)
	}
	if written := secondTransport.written(); len(written) != 0 {
		t.Fatalf("resume wrote to the wrong target: %q", written)
	}
}
