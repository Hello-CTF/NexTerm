package terminal

import "strings"

// keySequences maps semantic key names to the bytes sent to the peer.
// Inputs not in this table are sent as-is.
var keySequences = map[string][]byte{
	"enter":      {'\r'},
	"return":     {'\r'},
	"linefeed":   {'\n'},
	"lf":         {'\n'},
	"tab":        {'\t'},
	"shift+tab":  {0x1b, '[', 'Z'},
	"backtab":    {0x1b, '[', 'Z'},
	"esc":        {0x1b},
	"escape":     {0x1b},
	"space":      {' '},
	"backspace":  {0x7f},
	"delete":     {0x1b, '[', '3', '~'},
	"del":        {0x1b, '[', '3', '~'},
	"insert":     {0x1b, '[', '2', '~'},
	"home":       {0x1b, '[', 'H'},
	"end":        {0x1b, '[', 'F'},
	"pageup":     {0x1b, '[', '5', '~'},
	"pagedown":   {0x1b, '[', '6', '~'},
	"up":         {0x1b, '[', 'A'},
	"down":       {0x1b, '[', 'B'},
	"right":      {0x1b, '[', 'C'},
	"left":       {0x1b, '[', 'D'},
	"ctrl+@":     {0x00},
	"ctrl+space": {0x00},
	"ctrl+a":     {0x01},
	"ctrl+b":     {0x02},
	"ctrl+c":     {0x03},
	"ctrl+d":     {0x04},
	"ctrl+e":     {0x05},
	"ctrl+f":     {0x06},
	"ctrl+g":     {0x07},
	"ctrl+h":     {0x08},
	"ctrl+i":     {0x09},
	"ctrl+j":     {0x0a},
	"ctrl+k":     {0x0b},
	"ctrl+l":     {0x0c},
	"ctrl+m":     {0x0d},
	"ctrl+n":     {0x0e},
	"ctrl+o":     {0x0f},
	"ctrl+p":     {0x10},
	"ctrl+q":     {0x11},
	"ctrl+r":     {0x12},
	"ctrl+s":     {0x13},
	"ctrl+t":     {0x14},
	"ctrl+u":     {0x15},
	"ctrl+v":     {0x16},
	"ctrl+w":     {0x17},
	"ctrl+x":     {0x18},
	"ctrl+y":     {0x19},
	"ctrl+z":     {0x1a},
	"ctrl+\\":    {0x1c},
	"ctrl+]":     {0x1d},
	"ctrl+^":     {0x1e},
	"ctrl+_":     {0x1f},
	"f1":         {0x1b, 'O', 'P'},
	"f2":         {0x1b, 'O', 'Q'},
	"f3":         {0x1b, 'O', 'R'},
	"f4":         {0x1b, 'O', 'S'},
	"f5":         {0x1b, '[', '1', '5', '~'},
	"f6":         {0x1b, '[', '1', '7', '~'},
	"f7":         {0x1b, '[', '1', '8', '~'},
	"f8":         {0x1b, '[', '1', '9', '~'},
	"f9":         {0x1b, '[', '2', '0', '~'},
	"f10":        {0x1b, '[', '2', '1', '~'},
	"f11":        {0x1b, '[', '2', '3', '~'},
	"f12":        {0x1b, '[', '2', '4', '~'},
}

// EncodeKey translates a semantic key name (case-insensitive, surrounding
// whitespace ignored) into its byte sequence. It returns nil for unknown
// names.
func EncodeKey(name string) []byte {
	return keySequences[strings.ToLower(strings.TrimSpace(name))]
}

// EncodeSend combines literal text with <key> references and an optional
// trailing carriage return:
//
//	EncodeSend("y", true)          -> "y\r"
//	EncodeSend("<ctrl+c>", false)  -> "\x03"
//	EncodeSend("ls<enter>", false) -> "ls\r"
//
// Unknown tags are sent literally, angle brackets included.
//
// The <...> syntax is only a key-name shorthand for AI send_keys style
// input. It is NOT an escaping or security boundary: no input is sanitized
// here, and callers must not treat tag handling as a safety check.
func EncodeSend(text string, enter bool) []byte {
	out := make([]byte, 0, len(text)+1)
	rest := text
	for {
		start := strings.IndexByte(rest, '<')
		end := strings.IndexByte(rest, '>')
		if start < 0 || end < 0 {
			break
		}
		if start >= end {
			// A '>' before the next '<' disables tag parsing for
			// the remainder; everything is sent literally.
			break
		}
		out = append(out, rest[:start]...)
		if seq := EncodeKey(rest[start+1 : end]); seq != nil {
			out = append(out, seq...)
		} else {
			out = append(out, rest[start:end+1]...)
		}
		rest = rest[end+1:]
	}
	out = append(out, rest...)
	if enter {
		out = append(out, '\r')
	}
	return out
}
