package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/coder/websocket"
)

const (
	// channelInputSubprotocol is offered by clients that can send terminal input over the channel socket. It is negotiated
	// as a WebSocket subprotocol so older servers (which only know "nexterm") and older clients keep working unchanged.
	channelInputSubprotocol = "nexterm.input"
	channelSubprotocol      = "nexterm"

	// channelInputFrame is the type byte of an upstream input frame: [type][tabIdLen][tabId][data...].
	channelInputFrame byte = 1
	// channelInputMaxData bounds one frame's payload; clients split larger writes into ordered frames.
	channelInputMaxData = 16 << 10
	maxChannelClientID  = 128
)

// inputLimits is the per-connection input budget. Every frame re-runs session validation, so an unbounded sender could
// turn the socket into a cheap auth-check flood; the limits are far above human typing and large pastes.
type inputLimits struct {
	FrameBurst, FrameRate float64 // frames, frames per second
	ByteBurst, ByteRate   float64 // bytes, bytes per second
}

var (
	channelInputLimits = inputLimits{FrameBurst: 64, FrameRate: 200, ByteBurst: 1 << 20, ByteRate: 1 << 20}
	channelInputClock  = time.Now
)

type inputLimiter struct {
	mu             sync.Mutex
	limits         inputLimits
	now            func() time.Time
	last           time.Time
	frames, octets float64
}

func newInputLimiter(limits inputLimits, now func() time.Time) *inputLimiter {
	return &inputLimiter{limits: limits, now: now, last: now(), frames: limits.FrameBurst, octets: limits.ByteBurst}
}

// allow spends one frame and size bytes, reporting false when either budget is exhausted.
func (l *inputLimiter) allow(size int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	elapsed := now.Sub(l.last).Seconds()
	l.last = now
	if elapsed > 0 {
		l.frames = min(l.limits.FrameBurst, l.frames+elapsed*l.limits.FrameRate)
		l.octets = min(l.limits.ByteBurst, l.octets+elapsed*l.limits.ByteRate)
	}
	if l.frames < 1 || l.octets < float64(size) {
		return false
	}
	l.frames--
	l.octets -= float64(size)
	return true
}

// channelInputReader returns the upstream frame handler for a channel socket, or nil when the peer did not negotiate
// input. Frames are applied synchronously on the read goroutine, so they reach the terminal in send order. Each frame
// goes through requireAuth exactly like a POST /rpc terminal_write (session revalidation, CSRF-equivalent, identity
// context), so a revoked session stops input and the socket is dropped.
func (s *Server) channelInputReader(r *http.Request, connection *websocket.Conn, identity *account.Identity, clientID string) func(context.Context, websocket.MessageType, []byte) {
	if connection.Subprotocol() != channelInputSubprotocol {
		return nil
	}
	var call struct {
		tabID string
		data  []byte
	}
	handler := s.requireAuth(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		environment := s.environment
		if clientID != "" {
			environment.ClientID = clientID
		}
		s.dispatcher.Dispatch(req.Context(), ipc.Request{
			Command:  "terminal_write",
			Args:     terminalWriteArgs(call.tabID, call.data, clientID),
			ClientID: clientID,
		}, environment)
	}))
	limiter := newInputLimiter(channelInputLimits, channelInputClock)
	return func(ctx context.Context, kind websocket.MessageType, data []byte) {
		if kind != websocket.MessageBinary {
			return
		}
		if !limiter.allow(len(data)) {
			// Close handshakes read from this goroutine, so it must not wait here.
			go connection.Close(websocket.StatusPolicyViolation, "input rate exceeded")
			return
		}
		tabID, payload, ok := parseChannelInput(data)
		if !ok {
			_ = connection.CloseNow()
			return
		}
		call.tabID, call.data = tabID, payload
		req := r.Clone(ctx)
		req.Method = http.MethodPost
		req.Header = req.Header.Clone()
		if identity != nil {
			req.Header.Set(csrfHeaderName, accountCSRFToken(identity.SessionID))
		}
		recorder := &statusRecorder{header: http.Header{}}
		handler.ServeHTTP(recorder, req)
		if recorder.status >= http.StatusBadRequest {
			_ = connection.CloseNow()
		}
	}
}

func parseChannelInput(frame []byte) (tabID string, data []byte, ok bool) {
	if len(frame) < 3 || frame[0] != channelInputFrame {
		return "", nil, false
	}
	n := int(frame[1])
	if n == 0 || len(frame) < 2+n+1 || len(frame)-2-n > channelInputMaxData {
		return "", nil, false
	}
	return string(frame[2 : 2+n]), frame[2+n:], true
}

// terminalWriteArgs renders the nested terminal_write argument envelope; data uses the JSON number array the command expects.
func terminalWriteArgs(tabID string, data []byte, clientID string) []byte {
	out := make([]byte, 0, len(data)*4+len(tabID)+len(clientID)+48)
	out = append(out, `{"args":{"tabId":`...)
	quotedTab, _ := json.Marshal(tabID)
	out = append(out, quotedTab...)
	out = append(out, `,"clientId":`...)
	quotedClient, _ := json.Marshal(clientID)
	out = append(out, quotedClient...)
	out = append(out, `,"data":[`...)
	for i, b := range data {
		if i > 0 {
			out = append(out, ',')
		}
		out = strconv.AppendUint(out, uint64(b), 10)
	}
	return append(out, "]}}"...)
}

type statusRecorder struct {
	header http.Header
	status int
}

func (r *statusRecorder) Header() http.Header         { return r.header }
func (r *statusRecorder) Write(p []byte) (int, error) { return len(p), nil }
func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}
