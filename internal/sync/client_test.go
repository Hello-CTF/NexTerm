package sync

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestPushCarriesSnippetsWithoutAssets(t *testing.T) {
	ctx := context.Background()
	source := newTestInstance(t, false)
	target := newTestInstance(t, false)
	targetToken, err := target.service.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(target.service.PeerHandler())
	t.Cleanup(server.Close)
	if _, err := source.service.LinkSet(ctx, LinkPatch{URL: server.URL, Token: &targetToken}); err != nil {
		t.Fatal(err)
	}
	snippet, err := source.db.SnippetCreate(ctx, "deploy", "systemctl restart app", nil, 2)
	if err != nil {
		t.Fatal(err)
	}

	client := NewClient(source.service)
	report, err := client.Push(ctx, PushRequest{})
	if err != nil || report.SnippetsCreated != 1 || report.AssetsCreated != 0 {
		t.Fatalf("snippet-only push report=%+v err=%v", report, err)
	}
	imported, err := target.db.SnippetGet(ctx, snippet.ID)
	if err != nil || imported.Body != snippet.Body || imported.Name != snippet.Name || imported.Sort != snippet.Sort {
		t.Fatalf("pushed snippet=%+v err=%v", imported, err)
	}

	report, err = client.Push(ctx, PushRequest{})
	if err != nil || report.SnippetsUpdated != 1 || report.SnippetsCreated != 0 {
		t.Fatalf("idempotent snippet push report=%+v err=%v", report, err)
	}
	snippets, _ := target.db.SnippetList(ctx)
	if len(snippets) != 1 {
		t.Fatalf("repeat push duplicated snippets: %+v", snippets)
	}
}
