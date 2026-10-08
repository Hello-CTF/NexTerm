package hitl

import "encoding/json"

func WithNonce(raw json.RawMessage, nonce string) json.RawMessage {
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if fields == nil {
		fields = map[string]any{}
	}
	fields["confirmationNonce"] = nonce
	encoded, _ := json.Marshal(fields)
	return encoded
}
