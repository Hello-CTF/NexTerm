package psquote

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf16"
)

// Encode returns script as the base64 UTF-16LE payload that powershell -EncodedCommand expects. The base64
// alphabet has no character that cmd.exe or a POSIX shell treats specially, so the payload needs no quoting.
func Encode(script string) string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, 2*len(units))
	for index, unit := range units {
		binary.LittleEndian.PutUint16(raw[2*index:], unit)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// Decode reverses Encode.
func Decode(payload string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", err
	}
	if len(raw)%2 != 0 {
		return "", errors.New("encoded PowerShell payload has an odd byte count")
	}
	units := make([]uint16, len(raw)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(raw[2*index:])
	}
	return string(utf16.Decode(units)), nil
}

// Command runs script through shell with -EncodedCommand, without the logo, profile or prompts.
func Command(shell, script string) string {
	return shell + " -NoLogo -NoProfile -NonInteractive -EncodedCommand " + Encode(script)
}

// ShortCommand runs script through powershell on PATH; it omits -NoLogo, which -EncodedCommand already implies.
func ShortCommand(script string) string {
	return "powershell -NoProfile -NonInteractive -EncodedCommand " + Encode(script)
}

// DecodeCommand returns the script carried by a Command or ShortCommand string.
func DecodeCommand(command string) (string, error) {
	fields := strings.Fields(command)
	if len(fields) < 2 || !strings.EqualFold(fields[len(fields)-2], "-EncodedCommand") {
		return "", errors.New("not an -EncodedCommand PowerShell command")
	}
	return Decode(fields[len(fields)-1])
}
