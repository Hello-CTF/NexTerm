package tools

import (
	"encoding/json"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
)

func DisplaySendKeys(call Call, buffered string, cursor int) string {
	var input SendKeysArgs
	if json.Unmarshal(call.Args, &input) == nil {
		commands, controls, submits := guard.EffectiveKeyActions(input.Keys, buffered, cursor, input.Enter)
		parts := append([]string(nil), commands...)
		if len(controls) != 0 {
			parts = append(parts, "["+strings.Join(controls, " ")+"]")
		}
		if submits {
			parts = append(parts, "<enter>")
		}
		display := call.Name
		if len(parts) != 0 {
			display += " " + strings.Join(parts, " ")
		}
		return compactDisplay(display)
	}
	return DisplayCall(call)
}

func DisplayCall(call Call) string {
	var fields map[string]any
	_ = json.Unmarshal(call.Args, &fields)
	display := call.Name
	for _, key := range []string{"commands", "keys", "path", "container_id", "cmd", "action", "sql", "pattern", "question", "plan", "message"} {
		if value, ok := fields[key]; ok {
			encoded, _ := json.Marshal(value)
			display += " " + string(encoded)
			break
		}
	}
	return compactDisplay(display)
}

func compactDisplay(display string) string {
	display = strings.Join(strings.Fields(display), " ")
	runes := []rune(display)
	if len(runes) > 200 {
		display = string(runes[:60]) + "…" + string(runes[len(runes)-139:])
	}
	return display
}
