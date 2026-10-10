package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	"github.com/coder/websocket"
)

const (
	frameHello           = 1
	frameHelloAck        = 2
	frameError           = 3
	frameCreate          = 4
	frameCreated         = 5
	frameAttach          = 8
	frameAttached        = 9
	frameOK              = 13
	frameDetach          = 18
	frameOutput          = 19
	frameKillSession     = 22
	supervisorProtocolV2 = 2
)

type wireError struct {
	code    string
	message string
}

func (e *wireError) Error() string {
	return e.code + ": " + e.message
}

type supervisorStream struct {
	ws  *wsConn
	buf []byte
}

func (s *supervisorStream) sendFrame(kind byte, message interface{}) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	frame := make([]byte, 5, 5+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	frame[4] = kind
	return s.ws.send(websocket.MessageBinary, append(frame, payload...))
}

func (s *supervisorStream) readFrame(timeout time.Duration) (byte, []byte, error) {
	deadline := time.Now().Add(timeout)
	for {
		if len(s.buf) >= 5 {
			length := int(binary.BigEndian.Uint32(s.buf[:4]))
			if len(s.buf) >= 5+length {
				kind := s.buf[4]
				payload := append([]byte(nil), s.buf[5:5+length]...)
				s.buf = s.buf[5+length:]
				return kind, payload, nil
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, nil, fmt.Errorf("supervisor frame read timed out")
		}
		message, err := s.ws.recv(remaining)
		if err != nil {
			return 0, nil, fmt.Errorf("supervisor frame read timed out")
		}
		if message.err != nil {
			return 0, nil, fmt.Errorf("relay websocket closed mid-stream")
		}
		if message.kind == websocket.MessageBinary {
			s.buf = append(s.buf, message.payload...)
		}
	}
}

func (s *supervisorStream) hello(stateDigest string) error {
	if err := s.sendFrame(frameHello, map[string]interface{}{"version": supervisorProtocolV2, "state_digest": stateDigest}); err != nil {
		return err
	}
	kind, payload, err := s.readFrame(10 * time.Second)
	if err != nil {
		return err
	}
	if kind != frameHelloAck {
		return fmt.Errorf("supervisor hello failed: frame %d payload=%s", kind, payload)
	}
	return nil
}

func (s *supervisorStream) create(command []string, cols, rows int) (map[string]interface{}, error) {
	if err := s.sendFrame(frameCreate, map[string]interface{}{"command": command, "cols": cols, "rows": rows}); err != nil {
		return nil, err
	}
	kind, payload, err := s.readFrame(10 * time.Second)
	if err != nil {
		return nil, err
	}
	if kind == frameError {
		return nil, fmt.Errorf("create failed: %s", payload)
	}
	if kind != frameCreated {
		return nil, fmt.Errorf("create: unexpected frame %d", kind)
	}
	info := map[string]interface{}{}
	if err := json.Unmarshal(payload, &info); err != nil {
		return nil, err
	}
	return info, nil
}

func (s *supervisorStream) attach(sessionID string, expectCreatedAtNs int64, expectIncarnation string) (map[string]interface{}, error) {
	message := map[string]interface{}{"id": sessionID}
	if expectCreatedAtNs != 0 {
		message["expect_created_at_unix_nano"] = expectCreatedAtNs
	}
	if expectIncarnation != "" {
		message["expect_incarnation"] = expectIncarnation
	}
	if err := s.sendFrame(frameAttach, message); err != nil {
		return nil, err
	}
	kind, payload, err := s.readFrame(10 * time.Second)
	if err != nil {
		return nil, err
	}
	if kind == frameError {
		parsed := map[string]interface{}{}
		json.Unmarshal(payload, &parsed)
		return nil, &wireError{code: getStr(parsed, "code"), message: getStr(parsed, "message")}
	}
	if kind != frameAttached {
		return nil, fmt.Errorf("attach: unexpected frame %d", kind)
	}
	info := map[string]interface{}{}
	if err := json.Unmarshal(payload, &info); err != nil {
		return nil, err
	}
	return info, nil
}

func (s *supervisorStream) detach() error {
	return s.sendFrame(frameDetach, map[string]interface{}{})
}

func (s *supervisorStream) killSession(sessionID string) error {
	if err := s.sendFrame(frameKillSession, map[string]interface{}{"id": sessionID}); err != nil {
		return err
	}
	kind, payload, err := s.readFrame(10 * time.Second)
	if err != nil {
		return err
	}
	if kind == frameError {
		return fmt.Errorf("kill_session failed: %s", payload)
	}
	if kind != frameOK {
		return fmt.Errorf("kill_session: unexpected frame %d", kind)
	}
	return nil
}

func (s *supervisorStream) readOutput(timeout time.Duration) ([]byte, error) {
	kind, payload, err := s.readFrame(timeout)
	if err != nil {
		return nil, err
	}
	if kind == frameOutput {
		if len(payload) < 8 {
			return nil, fmt.Errorf("output frame too short: %d bytes", len(payload))
		}
		return payload[8:], nil
	}
	if kind == frameError {
		return nil, fmt.Errorf("stream error: %s", payload)
	}
	return nil, fmt.Errorf("unexpected frame %d while reading output", kind)
}
