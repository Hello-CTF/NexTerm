package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

func TestAssetSearchSpecialCharactersGroupsAndTags(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	parent, err := db.GroupCreate(ctx, GroupInput{Name: "生产"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := db.GroupCreate(ctx, GroupInput{Name: "应用", ParentID: &parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	percent, err := db.AssetCreate(ctx, AssetInput{
		Kind: "ssh", Name: "web%01", Host: ptr("10.0.0.1"), OptionsJSON: "{}",
		Tags: " Prod, blue, prod ", Note: "  first\nsecond\nline  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if percent.Tags != "Prod, blue" || percent.Note != "  first\nsecond\nline  " {
		t.Fatalf("create tags/note = %q / %q", percent.Tags, percent.Note)
	}
	underscore, err := db.AssetCreate(ctx, AssetInput{
		GroupID: &child.ID, Kind: "ssh", Name: "web_01", Host: ptr("10.0.0.2"), OptionsJSON: "{}",
		Tags: "production", Note: "child note",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: "plain", OptionsJSON: "{}", Tags: "productional"}); err != nil {
		t.Fatal(err)
	}

	assertNames := func(query string, want ...string) {
		t.Helper()
		rows, err := db.AssetSearch(ctx, query)
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		if len(rows) != len(want) {
			t.Fatalf("search %q returned %d rows (%+v), want %d", query, len(rows), rows, len(want))
		}
		got := map[string]bool{}
		for _, row := range rows {
			got[row.Name] = true
		}
		for _, name := range want {
			if !got[name] {
				t.Fatalf("search %q missing %q: %+v", query, name, rows)
			}
		}
	}
	assertNames(`web\%01`, "web%01")
	assertNames(`web\_01`, "web_01")
	assertNames("web%", "web%01", "web_01")
	assertNames("应用", "web_01")
	assertNames("生产", "web_01")
	assertNames("tag:prod", "web%01")
	assertNames("tag=PRODUCTION", "web_01")
	assertNames("tag:production%")
	assertNames("second\nline", "web%01")

	updated, err := db.AssetUpdate(ctx, percent.ID, AssetPatch{
		Tags: ptr(" blue, BLUE, new "), Note: ptr(" note with spaces \n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Tags != "blue, new" || updated.Note != " note with spaces \n" {
		t.Fatalf("update tags/note = %q / %q", updated.Tags, updated.Note)
	}
	if _, err := db.AssetGet(ctx, underscore.ID); err != nil {
		t.Fatal(err)
	}
}

func TestAssetSearchReportsTotalAndTruncation(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	for i := 0; i < AssetSearchLimit+1; i++ {
		if _, err := db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: fmt.Sprintf("bulk-%03d", i), OptionsJSON: "{}"}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := db.AssetSearchWithMeta(ctx, "bulk")
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != AssetSearchLimit+1 || len(result.Assets) != AssetSearchLimit || !result.Truncated {
		t.Fatalf("search metadata = total %d, returned %d, truncated %v", result.Total, len(result.Assets), result.Truncated)
	}
	rows, err := db.AssetSearch(ctx, "bulk")
	if err != nil || len(rows) != AssetSearchLimit {
		t.Fatalf("compatible search returned %d rows, err=%v", len(rows), err)
	}
}

func TestAssetSyncNormalizesTagsAndPreservesNote(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	row := AssetRow{
		ID: ids.New(), Kind: "ssh", Name: "legacy", OptionsJSON: "{}",
		Tags: " legacy,  legacy, mixed separators ", Note: "  preserve\nthis exactly  ", CreatedAt: 1, UpdatedAt: 2,
	}
	created, err := db.AssetUpsert(ctx, row)
	if err != nil || !created {
		t.Fatalf("sync upsert created=%v err=%v", created, err)
	}
	got, err := db.AssetGet(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Sync writes share the storage tag conventions (trim, case-insensitive dedup,
	// comma normalization); free-form fields stay byte-identical.
	if got.Tags != "legacy, mixed separators" || got.Note != row.Note {
		t.Fatalf("sync changed fields: tags %q / note %q", got.Tags, got.Note)
	}
}

func TestAssetSearchUnicodeCodePointsAndCaseFold(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	if _, err := db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: "💥", OptionsJSON: "{}", Tags: "ſ"}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.AssetSearch(ctx, "_")
	if err != nil || len(rows) != 1 {
		t.Fatalf("single code point search = %+v, err=%v", rows, err)
	}
	rows, err = db.AssetSearch(ctx, "__")
	if err != nil || len(rows) != 0 {
		t.Fatalf("two code point search = %+v, err=%v", rows, err)
	}
	rows, err = db.AssetSearch(ctx, "tag:s")
	if err != nil || len(rows) != 1 {
		t.Fatalf("Unicode case-fold tag search = %+v, err=%v", rows, err)
	}
}
