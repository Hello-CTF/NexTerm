package relaymux

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// maxFrame bounds a Hello, StreamHeader or Reply on the wire.
const maxFrame = 64 << 10

// Hello authenticates a session on its control stream; the dialer sends it first.
//
// A client presents its account session token, the value of the nexterm_session cookie the HTTPS API uses.
// The token travels only inside this frame, never as an ambient cookie, so a cross-site page cannot make a
// browser open an authenticated session and no CSRF token is needed (the websocket upgrade still applies the
// default Origin check). An agent presents the device id and secret of its /ws/device hello together with its
// control protocol version.
type Hello struct {
	Version  int    `json:"v"`
	Role     string `json:"role"`
	Token    string `json:"token,omitempty"`
	DeviceID string `json:"device_id,omitempty"`
	Secret   string `json:"secret,omitempty"`
	// Protocol is the agent control protocol version (agent.ProtocolVersion).
	Protocol int `json:"protocol,omitempty"`
	// AppVersion is informational.
	AppVersion string `json:"app_version,omitempty"`
}

// StreamHeader opens every non-control stream. A client sends it to the server naming the device and the
// bridge purpose; the server forwards it, with the same request id, to the agent.
type StreamHeader struct {
	Purpose  string `json:"purpose"`
	DeviceID string `json:"device_id,omitempty"`
	// Params carry the per-purpose relay options of today's bridge query, e.g. ParamStream and ParamResume.
	Params map[string]string `json:"params,omitempty"`
	// RequestID correlates the client, server and agent logs of one stream; the server fills an empty one.
	RequestID string `json:"request_id,omitempty"`
}

// Reply answers a Hello or a StreamHeader, and is the goaway frame a server writes on the control stream
// before it ends a session. Status uses the HTTP status vocabulary of the bridge endpoint (200 accepted,
// 400/401/403/404/409/503 refused), Code the ipc error code and Message its user-facing text.
type Reply struct {
	Status  int    `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	// Reason refines a refusal without changing Code, like the control channel's forbidden frame.
	Reason        string            `json:"reason,omitempty"`
	ServerVersion string            `json:"server_version,omitempty"`
	Params        map[string]string `json:"params,omitempty"`
}

// OK reports whether the reply accepts.
func (r Reply) OK() bool { return r.Status == http.StatusOK }

// RefusedError is a refusal Reply returned as an error.
type RefusedError struct {
	Reply Reply
}

func (e *RefusedError) Error() string {
	if e.Reply.Message != "" {
		return e.Reply.Message
	}
	return fmt.Sprintf("relaymux: refused with status %d", e.Reply.Status)
}

// Refused returns the refusal reply carried by err.
func Refused(err error) (Reply, bool) {
	var refused *RefusedError
	if errors.As(err, &refused) {
		return refused.Reply, true
	}
	return Reply{}, false
}

// WriteFrame writes one length-prefixed JSON frame.
func WriteFrame(w io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(payload) > maxFrame {
		return errors.New("relaymux: frame too large")
	}
	buffer := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buffer, uint32(len(payload)))
	copy(buffer[4:], payload)
	_, err = w.Write(buffer)
	return err
}

// ReadFrame reads one length-prefixed JSON frame into value.
func ReadFrame(r io.Reader, value any) error {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(size[:])
	if length == 0 || length > maxFrame {
		return errors.New("relaymux: bad frame length")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return err
	}
	return json.Unmarshal(payload, value)
}

// readFrameWithin reads a frame from conn, giving up after timeout.
func readFrameWithin(conn net.Conn, timeout time.Duration, value any) error {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	err := ReadFrame(conn, value)
	_ = conn.SetReadDeadline(time.Time{})
	return err
}

// ReadHeader reads the StreamHeader of an accepted stream.
func ReadHeader(conn net.Conn) (StreamHeader, error) {
	var header StreamHeader
	if err := readFrameWithin(conn, HandshakeTimeout, &header); err != nil {
		return StreamHeader{}, err
	}
	return header, nil
}

// ReadReply reads a Reply within HandshakeTimeout.
func ReadReply(conn net.Conn) (Reply, error) {
	return ReadReplyWithin(conn, HandshakeTimeout)
}

// ReadReplyWithin reads a Reply within timeout.
func ReadReplyWithin(conn net.Conn, timeout time.Duration) (Reply, error) {
	var reply Reply
	if err := readFrameWithin(conn, timeout, &reply); err != nil {
		return Reply{}, err
	}
	if reply.Status == 0 {
		return Reply{}, errors.New("relaymux: reply without status")
	}
	return reply, nil
}
