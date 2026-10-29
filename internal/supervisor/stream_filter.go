package supervisor

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"strings"
)

// CreateDefaults is what the host side of a bridge knows about the account that owns the helper: the
// shell and PATH a new terminal there should start with. A client on another machine cannot know them,
// and a helper that was started earlier (from an installer shell, or by an older build) would use its
// own stale environment, so the bridge fills them in as explicit per-session overrides.
type CreateDefaults struct {
	// Shell reports the shell for a new session; request is the SHELL the client itself asked for, which
	// wins when usable. An empty path disables the shell defaults.
	Shell func(request string) (path string, login bool)
	// Path reports the PATH of the user's login environment; empty disables it.
	Path func() string
}

// StreamFilter rewrites the client-to-helper byte stream of a bridge. It understands only the frame
// header (length and type): create frames get SHELL, PATH and, for an interactive terminal without a
// command, the login shell command added when the client did not set them; every other frame passes
// through unchanged and unbuffered. A hello that asks to replace a same-protocol helper of an older
// build is held back for the host to act on. The helper wire protocol is unchanged: the rewritten create
// frame is an ordinary one that every helper version honors.
type StreamFilter struct {
	defaults CreateDefaults

	header  [5]byte
	headerN int
	kind    frameType
	remain  int
	capture bool
	buffer  []byte
	passAll bool
	stopped bool
}

// NewStreamFilter returns a filter that applies defaults to create frames.
func NewStreamFilter(defaults CreateDefaults) *StreamFilter {
	return &StreamFilter{defaults: defaults}
}

// Filter consumes the next bytes of the stream and returns the bytes to forward to the helper. When the
// stream carried a same-protocol replacement hello, replaceToken is its consent token and nothing after
// it is forwarded.
func (f *StreamFilter) Filter(data []byte) (forward []byte, replaceToken string) {
	if f.stopped {
		return nil, ""
	}
	if f.passAll {
		return data, ""
	}
	for len(data) > 0 {
		if f.headerN < len(f.header) {
			count := copy(f.header[f.headerN:], data)
			f.headerN += count
			data = data[count:]
			if f.headerN < len(f.header) {
				break
			}
			length := binary.BigEndian.Uint32(f.header[:4])
			if length > maxFramePayload {
				// Not a stream this filter understands: stop interpreting it.
				f.passAll = true
				forward = append(forward, f.header[:]...)
				return append(forward, data...), ""
			}
			f.kind = frameType(f.header[4])
			f.remain = int(length)
			f.capture = f.kind == frameCreate || f.kind == frameHello
			f.buffer = f.buffer[:0]
			if !f.capture {
				forward = append(forward, f.header[:]...)
			}
			if f.remain == 0 {
				var token string
				forward, token = f.finishFrame(forward)
				if token != "" {
					return forward, token
				}
			}
			continue
		}
		count := min(f.remain, len(data))
		chunk := data[:count]
		data = data[count:]
		f.remain -= count
		if f.capture {
			f.buffer = append(f.buffer, chunk...)
		} else {
			forward = append(forward, chunk...)
		}
		if f.remain == 0 {
			var token string
			forward, token = f.finishFrame(forward)
			if token != "" {
				return forward, token
			}
		}
	}
	return forward, ""
}

func (f *StreamFilter) finishFrame(forward []byte) ([]byte, string) {
	defer func() { f.headerN = 0 }()
	if !f.capture {
		return forward, ""
	}
	payload := f.buffer
	switch f.kind {
	case frameHello:
		var hello helloMsg
		if json.Unmarshal(payload, &hello) == nil && hello.ReplaceLegacy != "" {
			if consent, ok := ParseHelperConsent(hello.ReplaceLegacy); ok && consent.Protocol == ProtocolVersion {
				f.stopped = true
				return forward, hello.ReplaceLegacy
			}
		}
	case frameCreate:
		payload = f.applyCreateDefaults(payload)
	}
	return appendFrame(forward, f.kind, payload), ""
}

func appendFrame(dst []byte, kind frameType, payload []byte) []byte {
	var header [5]byte
	binary.BigEndian.PutUint32(header[:4], uint32(len(payload)))
	header[4] = byte(kind)
	dst = append(dst, header[:]...)
	return append(dst, payload...)
}

// applyCreateDefaults returns the create payload with the host defaults added. Unknown fields are kept,
// and anything it cannot interpret is returned untouched.
func (f *StreamFilter) applyCreateDefaults(payload []byte) []byte {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return payload
	}
	var env, command []string
	if raw, ok := fields["env"]; ok && json.Unmarshal(raw, &env) != nil {
		return payload
	}
	if raw, ok := fields["command"]; ok && json.Unmarshal(raw, &command) != nil {
		return payload
	}
	interactive := true
	if raw, ok := fields["pty"]; ok {
		var pty *bool
		if json.Unmarshal(raw, &pty) != nil {
			return payload
		}
		interactive = pty == nil || *pty
	}
	changed := false
	if f.defaults.Shell != nil {
		request := environmentValue(env, "SHELL")
		if shell, login := f.defaults.Shell(request); shell != "" {
			if request == "" {
				env = append(env, "SHELL="+shell)
				changed = true
			}
			if interactive && len(command) == 0 {
				command = []string{shell}
				if login {
					command = append(command, "-l")
				}
				changed = true
			}
		}
	}
	if f.defaults.Path != nil && environmentValue(env, "PATH") == "" {
		if path := f.defaults.Path(); path != "" && !strings.ContainsAny(path, "\x00") {
			env = append(env, "PATH="+path)
			changed = true
		}
	}
	if !changed {
		return payload
	}
	if encoded, err := json.Marshal(env); err == nil {
		fields["env"] = encoded
	} else {
		return payload
	}
	if len(command) > 0 {
		if encoded, err := json.Marshal(command); err == nil {
			fields["command"] = encoded
		}
	}
	out, err := json.Marshal(fields)
	if err != nil || len(out) > maxFramePayload {
		return payload
	}
	return out
}

// ReplaceStaleHelperFrame performs the same-protocol replacement a bridge intercepted and returns the
// frame that answers the client's hello: frameOK when this build's helper serves, otherwise an error
// frame carrying why. The consent rules are those of ReplaceStaleHelper.
func ReplaceStaleHelperFrame(ctx context.Context, config HelperConfig, token string) []byte {
	_, result, err := ReplaceStaleHelper(ctx, config, token)
	if err != nil {
		if encoded, marshalErr := marshalFrame(frameError, replaceErrorMessage(err)); marshalErr == nil {
			return appendFrame(nil, frameError, encoded)
		}
		return nil
	}
	encoded, marshalErr := marshalFrame(frameOK, replacedMsg{Stopped: result.Stopped, Forced: result.Forced})
	if marshalErr != nil {
		return nil
	}
	return appendFrame(nil, frameOK, encoded)
}
