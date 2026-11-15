package takeover

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

func TestTakeoverWaitForLineAnchoredPattern(t *testing.T) {
	for _, test := range []struct {
		name  string
		tail  []string
		match bool
	}{
		{name: "line among others", tail: []string{"$ ./run.sh", "===DONE-E===", "$ "}, match: true},
		{name: "space-padded row", tail: []string{"===DONE-E===    ", "$ "}, match: true},
		{name: "mid-line only", tail: []string{"$ echo ===DONE-E=== now"}, match: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &Manager{deps: Dependencies{Snapshot: func(context.Context, string) (tools.Screen, error) {
				return tools.Screen{Tail: test.tail}, nil
			}}}
			output, err := manager.waitFor(context.Background(), "tab", "^===DONE-E===$", 1)
			if err != nil {
				t.Fatal(err)
			}
			if output.OK != test.match {
				t.Fatalf("matched = %v, want %v: %+v", output.OK, test.match, output)
			}
		})
	}
}
