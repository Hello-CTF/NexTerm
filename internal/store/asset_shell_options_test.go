package store

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestAssetShellArgsValidatedOnWrite(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	stored := `{"shell":"C:\\Windows\\System32\\wsl.exe","shellArgs":["-d","Ubuntu 24.04"]}`
	row, err := db.AssetCreate(ctx, AssetInput{Kind: "local", Name: "wsl", OptionsJSON: stored})
	if err != nil {
		t.Fatal(err)
	}
	if row.OptionsJSON != stored {
		t.Fatalf("options not stored as written: %s", row.OptionsJSON)
	}
	for _, options := range []string{`{"shell":"/bin/zsh"}`, `{"shellArgs":null}`, `{"shellArgs":[]}`} {
		value := options
		if _, err := db.AssetUpdate(ctx, row.ID, AssetPatch{OptionsJSON: &value}); err != nil {
			t.Fatalf("valid options %s rejected: %v", options, err)
		}
	}
	tooMany := `{"shellArgs":[` + strings.TrimSuffix(strings.Repeat(`"-x",`, maxShellArgs+1), ",") + `]}`
	for _, options := range []string{
		`{"shellArgs":"-d Ubuntu"}`,
		`{"shellArgs":[1]}`,
		`{"shellArgs":["-c","a\nb"]}`,
		`{"shellArgs":["` + strings.Repeat("a", maxShellArgLength+1) + `"]}`,
		tooMany,
	} {
		_, err := db.AssetCreate(ctx, AssetInput{Kind: "local", Name: "bad", OptionsJSON: options})
		requireCode(t, err, ipc.CodeBadParam)
		value := options
		_, err = db.AssetUpdate(ctx, row.ID, AssetPatch{OptionsJSON: &value})
		requireCode(t, err, ipc.CodeBadParam)
	}
}
