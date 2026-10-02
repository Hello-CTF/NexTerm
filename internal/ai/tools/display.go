package tools

import (
	"encoding/json"
	"strings"
)

func DisplayCall(call Call) string {
	var fields map[string]any
	_ = json.Unmarshal(call.Args, &fields)
	display := call.Name
	for _, key := range []string{"commands", "path", "container_id", "cmd", "action", "sql", "pattern", "question", "plan"} {
		if value, ok := fields[key]; ok {
			encoded, _ := json.Marshal(value)
			display += " " + string(encoded)
			break
		}
	}
	display = strings.Join(strings.Fields(display), " ")
	runes := []rune(display)
	if len(runes) > 200 {
		display = string(runes[:200]) + "…"
	}
	return display
}
