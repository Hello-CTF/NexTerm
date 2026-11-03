package agent

import "testing"

func TestRefSpecsCarryTextForCommandOutputReferences(t *testing.T) {
	specs := refSpecs([]RefArg{
		{Kind: "text", ID: "blk-1", Label: "ls", Text: "file-a\nfile-b"},
		{Kind: "tab", ID: "tab-1", Label: "t"},
	})
	if len(specs) != 2 || specs[0].Kind != "text" || specs[0].Text != "file-a\nfile-b" || specs[0].Label != "ls" || specs[1].Text != "" {
		t.Fatalf("unexpected specs: %+v", specs)
	}
}
