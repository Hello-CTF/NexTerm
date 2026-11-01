package store

import (
	"encoding/json"

	"github.com/Hello-CTF/NexTerm/internal/wol"
)

var wakeOptionKeys = []string{"wakeMac", "wakeBroadcast", "wakePort"}

// validateAssetWakeOptions checks the Wake-on-LAN options of a user-written asset. When current is non-empty (an
// edit), values that did not change are left alone, so an asset that already holds an odd value, for example from
// sync, can still be saved for unrelated reasons; only a changed wake value must be valid.
func validateAssetWakeOptions(raw, current string) error {
	if current != "" && wakeOptionsUnchanged(raw, current) {
		return nil
	}
	return wol.ValidateOptions(raw)
}

func wakeOptionsUnchanged(a, b string) bool {
	return wakeOptionValues(a) == wakeOptionValues(b)
}

func wakeOptionValues(raw string) string {
	var options map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		return ""
	}
	picked := map[string]json.RawMessage{}
	for _, key := range wakeOptionKeys {
		if value, ok := options[key]; ok && string(value) != "null" {
			picked[key] = value
		}
	}
	encoded, _ := json.Marshal(picked)
	return string(encoded)
}
