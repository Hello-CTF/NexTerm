package production

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestProductionFSEntryCarriesTargetKindAndHidden(t *testing.T) {
	link, err := json.Marshal(productionFSEntry(base.FileEntry{
		Name: ".link", Path: "/home/a/.link", Kind: base.FileSymlink, SymlinkTarget: "/srv", TargetKind: base.FileDirectory,
		Hidden: true, ModTime: time.Unix(1, 0),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(link), `"targetKind":"dir"`) || !strings.Contains(string(link), `"hidden":true`) {
		t.Fatalf("symlink DTO = %s", link)
	}
	plain, err := json.Marshal(productionFSEntry(base.FileEntry{Name: "file", Path: "/home/a/file", Kind: base.FileFile}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "targetKind") || strings.Contains(string(plain), "hidden") {
		t.Fatalf("plain DTO = %s, want targetKind and hidden omitted", plain)
	}
}
