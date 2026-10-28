package signals

import (
	"bytes"
	"encoding/base64"
	"strings"
	"time"
	"unicode"
)

// dispatch routes one complete OSC payload by its numeric code.
func (s *Scanner) dispatch(out []Event, payload []byte, now time.Time) []Event {
	code, rest, _ := bytes.Cut(payload, []byte(";"))
	switch string(code) {
	case "133":
		return s.command(out, payload, now)
	case "9":
		return s.osc9(out, rest, now)
	case "777":
		return s.osc777(out, rest, now)
	case "99":
		return s.osc99(out, rest, now)
	}
	return out
}

// osc9 handles `9;<text>`. ConEmu reuses OSC 9 for numbered subcommands
// (`9;4;...` progress, `9;1;...` sleep, `9;9;...` cwd and so on), which are
// not notifications and are filtered out. The filter also drops bodies that
// are only a number ("42", "2024;done"); that is a deliberate trade-off,
// counted in Stats.ConEmuFiltered.
func (s *Scanner) osc9(out []Event, text []byte, now time.Time) []Event {
	if len(text) > MaxNotifyPayload {
		s.stats.Oversize++
		return out
	}
	if isConEmuSubcommand(text) {
		s.stats.ConEmuFiltered++
		return out
	}
	return s.notify(out, SourceOSC9, nil, text, now)
}

func isConEmuSubcommand(text []byte) bool {
	n := 0
	for n < len(text) && text[n] >= '0' && text[n] <= '9' {
		n++
	}
	return n > 0 && (n == len(text) || text[n] == ';')
}

// osc777 handles the rxvt-unicode style `777;notify;<title>;<body>`. Other
// 777 subcommands (for example VTE's precmd and preexec) are ignored.
func (s *Scanner) osc777(out []Event, rest []byte, now time.Time) []Event {
	if len(rest) > MaxNotifyPayload {
		s.stats.Oversize++
		return out
	}
	parts := bytes.SplitN(rest, []byte(";"), 3)
	if string(parts[0]) != "notify" {
		return out
	}
	if len(parts) < 2 {
		s.stats.Malformed++
		return out
	}
	var body []byte
	if len(parts) == 3 {
		body = parts[2]
	}
	return s.notify(out, SourceOSC777, parts[1], body, now)
}

// assembly accumulates the chunks of one OSC 99 notification.
type assembly struct {
	id    string
	title []byte
	body  []byte
	dead  bool // exceeded the size limit; remaining chunks are discarded
}

// osc99 handles kitty's `99;<metadata>;<payload>`. Supported metadata keys:
// i (identifier), d (0 = more chunks follow, default 1), p (title or body,
// default title) and e (1 = base64 payload). Other payload types (close,
// alive, icon, queries) are not notifications and are ignored.
func (s *Scanner) osc99(out []Event, rest []byte, now time.Time) []Event {
	meta, payload, _ := bytes.Cut(rest, []byte(";"))
	var id, ptype string
	done, b64 := true, false
	for _, kv := range strings.Split(string(meta), ":") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch k {
		case "i":
			id = v
		case "d":
			done = v != "0"
		case "p":
			ptype = v
		case "e":
			b64 = v == "1"
		}
	}
	if ptype == "" {
		ptype = "title"
	}
	switch ptype {
	case "title", "body":
	case "close":
		s.dropAssembly(id)
		return out
	default:
		// Buttons, icons and queries carry no text, but a final chunk of
		// one of them still completes the notification built so far.
		if !done {
			return out
		}
		for _, a := range s.assemblies {
			if a.id == id {
				s.dropAssembly(id)
				if a.dead {
					return out
				}
				return s.notify(out, SourceOSC99, a.title, a.body, now)
			}
		}
		return out
	}
	if b64 {
		dec, err := base64.StdEncoding.DecodeString(string(payload))
		if err != nil {
			dec, err = base64.RawStdEncoding.DecodeString(string(payload))
		}
		if err != nil {
			s.stats.Malformed++
			s.dropAssembly(id)
			return out
		}
		payload = dec
	}

	a := s.assemblyFor(id)
	if !a.dead {
		if len(a.title)+len(a.body)+len(payload) > MaxAssembledPayload {
			a.dead = true
			a.title, a.body = nil, nil
			s.stats.Oversize++
		} else if ptype == "title" {
			a.title = append(a.title, payload...)
		} else {
			a.body = append(a.body, payload...)
		}
	}
	if !done {
		return out
	}
	s.dropAssembly(id)
	if a.dead {
		return out
	}
	return s.notify(out, SourceOSC99, a.title, a.body, now)
}

func (s *Scanner) assemblyFor(id string) *assembly {
	for _, a := range s.assemblies {
		if a.id == id {
			return a
		}
	}
	a := &assembly{id: id}
	if len(s.assemblies) >= MaxAssemblies {
		s.assemblies = append(s.assemblies[:0], s.assemblies[1:]...)
	}
	s.assemblies = append(s.assemblies, a)
	return a
}

func (s *Scanner) dropAssembly(id string) {
	for i, a := range s.assemblies {
		if a.id == id {
			s.assemblies = append(s.assemblies[:i], s.assemblies[i+1:]...)
			return
		}
	}
}

// clean makes notification text safe to display: invalid UTF-8 is replaced
// and control and format characters other than newline, tab, ZWJ, ZWNJ and
// emoji tag characters are removed.
func clean(v string) string {
	v = strings.ToValidUTF8(v, "�")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		// Drop controls and format characters (bidi overrides, zero-width
		// spaces), but keep ZWJ/ZWNJ which emoji and several scripts need and
		// the tag characters that form subdivision flags such as Scotland's.
		if unicode.IsControl(r) || (unicode.Is(unicode.Cf, r) && r != '\u200c' && r != '\u200d' && (r < 0xe0020 || r > 0xe007f)) {
			return -1
		}
		return r
	}, v)
}
