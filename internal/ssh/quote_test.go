package ssh

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

func decodePowerShellScript(t *testing.T, command string) string {
	t.Helper()
	fields := strings.Fields(command)
	if len(fields) == 0 {
		t.Fatal("empty PowerShell command")
	}
	raw, err := base64.StdEncoding.DecodeString(fields[len(fields)-1])
	if err != nil {
		t.Fatal(err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("odd UTF-16 payload length %d", len(raw))
	}
	units := make([]uint16, len(raw)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(raw[index*2:])
	}
	return string(utf16.Decode(units))
}

func TestPowerShellCommandRoundTripsQuotesAndUnicode(t *testing.T) {
	script := "$path = 'C:\\Users\\O''Brien\\助手'; & $path --probe"
	command := powerShellCommand("powershell.exe", script)
	if got := decodePowerShellScript(t, command); got != script {
		t.Fatalf("decoded script = %q, want %q", got, script)
	}
}

func TestQuotePowerShellLiteral(t *testing.T) {
	if got := quotePowerShell(`C:\O'Brien\helper.exe`); got != `'C:\O''Brien\helper.exe'` {
		t.Fatalf("quotePowerShell = %q", got)
	}
}

func TestQuoteWindowsArgumentPreservesSpacesAndQuotes(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "plain", want: "plain"},
		{input: `C:\daemon state`, want: `"C:\daemon state"`},
		{input: `a"b`, want: `"a\"b"`},
		{input: `trailing\`, want: `trailing\`},
		{input: `two slashes\\`, want: `"two slashes\\\\"`},
	} {
		if got := quoteWindowsArgument(test.input); got != test.want {
			t.Fatalf("quoteWindowsArgument(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}
