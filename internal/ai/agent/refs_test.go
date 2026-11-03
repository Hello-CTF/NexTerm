package agent

import (
	"reflect"
	"testing"

	aicontext "github.com/Hello-CTF/NexTerm/internal/ai/context"
)

func TestRefSpecsCarryTextForCommandOutputReferences(t *testing.T) {
	specs := refSpecs([]RefArg{
		{Kind: "text", ID: "blk-1", Label: "ls", Text: "file-a\nfile-b"},
		{Kind: "tab", ID: "tab-1", Label: "t"},
	})
	want := []aicontext.RefSpec{
		{Kind: "text", ID: "blk-1", Label: "ls", Text: "file-a\nfile-b"},
		{Kind: "tab", ID: "tab-1", Label: "t"},
	}
	if !reflect.DeepEqual(specs, want) {
		t.Fatalf("specs = %+v, want %+v", specs, want)
	}
}
