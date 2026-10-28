package store

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestNormalizePasteDir(t *testing.T) {
	cases := map[string]string{
		"":              "",
		"   ":           "",
		"/":             "/",
		"//":            "/",
		"/tmp/":         "/tmp",
		" /srv//up/./ ": "/srv/up",
		"/a b/c":        "/a b/c",
	}
	for input, want := range cases {
		got, err := normalizePasteDir(input)
		if err != nil || got != want {
			t.Fatalf("normalizePasteDir(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"tmp", "./tmp", "/tmp/../etc", "/..", "/tmp\n", "/tmp\x00x", "/a\u202eb", "C:\\Temp"} {
		if _, err := normalizePasteDir(input); err == nil {
			t.Fatalf("normalizePasteDir(%q) accepted an invalid path", input)
		}
	}
}

func TestAssetPasteOptionsValidatedOnWrite(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	row, err := db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: "chroot", Host: ptr("10.0.0.2"),
		OptionsJSON: `{"pasteUploadDir":"/","pasteTerminalDir":"/tmp/"}`})
	if err != nil {
		t.Fatal(err)
	}
	if row.OptionsJSON != `{"pasteUploadDir":"/","pasteTerminalDir":"/tmp/"}` {
		t.Fatalf("options not stored as written: %s", row.OptionsJSON)
	}
	for _, options := range []string{
		`{"pasteUploadDir":"tmp"}`,
		`{"pasteUploadDir":"/srv/../etc"}`,
		`{"pasteUploadDir":"/tmp\u0007"}`,
		`{"pasteUploadDir":42}`,
		`{"pasteTerminalDir":"/tmp"}`,
	} {
		_, err := db.AssetCreate(ctx, AssetInput{Kind: "ssh", Name: "bad", Host: ptr("10.0.0.3"), OptionsJSON: options})
		requireCode(t, err, ipc.CodeBadParam)
		value := options
		_, err = db.AssetUpdate(ctx, row.ID, AssetPatch{OptionsJSON: &value})
		requireCode(t, err, ipc.CodeBadParam)
	}
	cleared := `{"pasteUploadDir":""}`
	if _, err := db.AssetUpdate(ctx, row.ID, AssetPatch{OptionsJSON: &cleared}); err != nil {
		t.Fatal(err)
	}
}
