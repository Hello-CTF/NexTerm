package ipc

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type Request struct {
	Command  string          `json:"cmd"`
	Args     json.RawMessage `json:"args,omitempty"`
	Channel  ChannelRef      `json:"channel,omitempty"`
	ClientID string          `json:"clientId,omitempty"`
}

type Response struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *Error          `json:"error,omitempty"`
}

type ChannelRef struct {
	ID string
}

func (c ChannelRef) String() string {
	return c.ID
}

func (c ChannelRef) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.ID)
}

func (c *ChannelRef) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		c.ID = ""
		return nil
	}

	var id string
	if err := json.Unmarshal(data, &id); err == nil {
		c.ID = id
		return nil
	}

	var number json.Number
	if err := json.Unmarshal(data, &number); err == nil {
		c.ID = number.String()
		return nil
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err == nil {
		for _, key := range []string{"id", "channelId"} {
			if raw, ok := object[key]; ok {
				var nested ChannelRef
				if err := nested.UnmarshalJSON(raw); err != nil {
					return err
				}
				c.ID = nested.ID
				return nil
			}
		}
	}

	return fmt.Errorf("channel must be a string, number, or object with id/channelId")
}
