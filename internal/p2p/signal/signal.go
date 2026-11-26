// Package signal is the wire format shared by the client and the agent for direct connections: the JSON frames
// of the p2p-signal bridge (design 2.1) and the purpose preamble of every direct stream.
package signal

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Frame types of the signal bridge.
const (
	// TypeOffer opens a direct session (client to agent): the grant, the client key, ICE credentials and the
	// client's NAT classification. TypeAttach re-attaches a new signal bridge to the live session of Grant.
	TypeOffer  = "offer"
	TypeAttach = "attach"
	// TypeAnswer is the agent's ICE credentials and NAT classification, signed with the device key.
	TypeAnswer = "answer"
	// TypeCandidate trickles one ICE candidate, either way.
	TypeCandidate = "candidate"
	// TypeGrantRefresh hands the agent a renewed grant of the same session.
	TypeGrantRefresh = "grant_refresh"
	// TypePing and TypePong measure the relay round trip; Seq matches them.
	TypePing = "ping"
	TypePong = "pong"
	// TypeClose ends the direct session; Reason says why.
	TypeClose = "close"
)

// Close reasons the agent sends.
const (
	CloseRevoked  = "revoked"
	CloseExpired  = "expired"
	CloseRefused  = "refused"
	CloseShutdown = "shutdown"
	CloseIdle     = "idle"
)

// MaxFrame bounds one frame's JSON; a grant is under 4 KiB and a candidate a few hundred bytes.
const MaxFrame = 64 << 10

// Frame is one message of the signal bridge. Fields not used by a type stay empty.
type Frame struct {
	Type      string `json:"type"`
	Grant     string `json:"grant,omitempty"`
	ClientPub string `json:"client_pub,omitempty"`
	Ufrag     string `json:"ufrag,omitempty"`
	Pwd       string `json:"pwd,omitempty"`
	NAT       string `json:"nat,omitempty"`
	// AgentPubSig signs the answer with the device key (AnswerSignature).
	AgentPubSig string `json:"agent_pub_sig,omitempty"`
	Candidate   string `json:"candidate,omitempty"`
	Seq         uint64 `json:"seq,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// Conn reads and writes frames on a byte stream: a 4-byte big-endian length, then the JSON. Writes are
// serialized; reads must come from one goroutine.
type Conn struct {
	conn net.Conn
	wmu  sync.Mutex
	rbuf [4]byte
}

// NewConn wraps conn.
func NewConn(conn net.Conn) *Conn { return &Conn{conn: conn} }

// Write sends one frame; ctx bounds the write.
func (c *Conn) Write(ctx context.Context, frame Frame) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(payload) > MaxFrame {
		return fmt.Errorf("signal: %s frame of %d bytes exceeds %d", frame.Type, len(payload), MaxFrame)
	}
	message := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(message, uint32(len(payload)))
	copy(message[4:], payload)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetWriteDeadline(time.Unix(1, 0)) })
	_, err = c.conn.Write(message)
	if !stop() {
		_ = c.conn.SetWriteDeadline(time.Time{})
		return errors.Join(ctx.Err(), err)
	}
	return err
}

// Read waits for the next frame.
func (c *Conn) Read() (Frame, error) {
	if _, err := io.ReadFull(c.conn, c.rbuf[:]); err != nil {
		return Frame{}, err
	}
	length := binary.BigEndian.Uint32(c.rbuf[:])
	if length == 0 || length > MaxFrame {
		return Frame{}, fmt.Errorf("signal: frame length %d out of range", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.conn, payload); err != nil {
		return Frame{}, err
	}
	var frame Frame
	if err := json.Unmarshal(payload, &frame); err != nil {
		return Frame{}, fmt.Errorf("signal: bad frame: %w", err)
	}
	if frame.Type == "" {
		return Frame{}, errors.New("signal: frame without a type")
	}
	return frame, nil
}

// Close closes the underlying stream.
func (c *Conn) Close() error { return c.conn.Close() }

const answerDomain = "nexterm-p2p-answer/1\x00"

func answerMessage(grantID, clientUfrag, ufrag, pwd, nat string) []byte {
	var message []byte
	message = append(message, answerDomain...)
	for _, part := range []string{grantID, clientUfrag, ufrag, pwd, nat} {
		message = binary.BigEndian.AppendUint16(message, uint16(len(part)))
		message = append(message, part...)
	}
	return message
}

// AnswerSignature signs an answer with the device key, binding it to the grant and the client's offer.
func AnswerSignature(key ed25519.PrivateKey, grantID, clientUfrag string, answer Frame) string {
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, answerMessage(grantID, clientUfrag, answer.Ufrag, answer.Pwd, answer.NAT)))
}

// VerifyAnswer checks an answer's signature against the device key the grant names.
func VerifyAnswer(agentPub ed25519.PublicKey, grantID, clientUfrag string, answer Frame) bool {
	signature, err := base64.RawURLEncoding.Strict().DecodeString(answer.AgentPubSig)
	if err != nil || len(signature) != ed25519.SignatureSize || len(agentPub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(agentPub, answerMessage(grantID, clientUfrag, answer.Ufrag, answer.Pwd, answer.NAT), signature)
}

// MaxPurpose bounds the purpose preamble.
const MaxPurpose = 64

// WritePurpose starts a direct stream with its purpose: one length byte, then the purpose. The agent checks it
// against the grant before it reads the rstream OPEN or RESUME that follows, since a RESUME names no purpose.
func WritePurpose(conn net.Conn, purpose string) error {
	if purpose == "" || len(purpose) > MaxPurpose {
		return fmt.Errorf("signal: purpose %q out of range", purpose)
	}
	_, err := conn.Write(append([]byte{byte(len(purpose))}, purpose...))
	return err
}

// ReadPurpose reads a direct stream's purpose preamble.
func ReadPurpose(conn net.Conn) (string, error) {
	var length [1]byte
	if _, err := io.ReadFull(conn, length[:]); err != nil {
		return "", err
	}
	if length[0] == 0 || int(length[0]) > MaxPurpose {
		return "", fmt.Errorf("signal: purpose length %d out of range", length[0])
	}
	purpose := make([]byte, length[0])
	if _, err := io.ReadFull(conn, purpose); err != nil {
		return "", err
	}
	return string(purpose), nil
}

// Candidates adapts the trickled candidates of a signal bridge to leg.Signaler: Push feeds received candidates,
// Finish ends them, and SendCandidate writes a candidate frame.
type Candidates struct {
	conn *Conn
	ch   chan string
	once sync.Once
	done chan struct{}
}

// NewCandidates returns the adapter writing to conn.
func NewCandidates(conn *Conn) *Candidates {
	return &Candidates{conn: conn, ch: make(chan string, 64), done: make(chan struct{})}
}

// SendCandidate implements leg.Signaler.
func (c *Candidates) SendCandidate(ctx context.Context, candidate string) error {
	return c.conn.Write(ctx, Frame{Type: TypeCandidate, Candidate: candidate})
}

// Candidates implements leg.Signaler.
func (c *Candidates) Candidates() <-chan string { return c.ch }

// Push delivers a received candidate; it drops candidates beyond the buffer, which only loses a path.
func (c *Candidates) Push(candidate string) {
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case c.ch <- candidate:
	default:
	}
}

// Finish closes the candidate channel; Push and Finish must come from the one reader goroutine.
func (c *Candidates) Finish() {
	c.once.Do(func() {
		close(c.done)
		close(c.ch)
	})
}
