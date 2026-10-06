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
	if err := json.Unmarshal(data, &id); err != nil {
		return fmt.Errorf("channel must be a string")
	}
	c.ID = id
	return nil
}
