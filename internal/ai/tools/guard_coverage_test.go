package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

// Every built-in tool must get an explicit guard ruling; falling through to the guard's default shows users an
// "unknown tool" prompt for tools the registry itself advertises.
func TestEveryBuiltinToolHasGuardRuling(t *testing.T) {
	for _, name := range append([]string{"load_skill"}, einoNames...) {
		ruling := guard.ClassifyTool(name, json.RawMessage(`{}`), guard.Config{})
		if strings.Contains(ruling.Reason, "未知工具") {
			t.Errorf("%s has no guard ruling: %+v", name, ruling)
		}
	}
}
