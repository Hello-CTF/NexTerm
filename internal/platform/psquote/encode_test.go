package psquote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The golden payloads were produced by the encoders that hostprocs, hostports, ssh and transport/ssh used
// before they shared this one, so a match proves the commands sent to hosts did not change.
func TestEncodeMatchesGolden(t *testing.T) {
	cases := map[string]string{
		"ascii":   "Get-Service | ConvertTo-Json",
		"quotes":  "$path = 'C:\\Users\\O''Brien\\助手'; & $path --probe",
		"astral":  "Write-Output '\U0001F600'",
		"newline": "a\r\nb\n",
	}
	data, err := os.ReadFile(filepath.Join("testdata", "encode.golden"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		name, payload, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("malformed golden line %q", line)
		}
		want[name] = payload
	}
	for name, script := range cases {
		got := Encode(script)
		if got != want[name] {
			t.Errorf("Encode(%s) = %s, want %s", name, got, want[name])
		}
		decoded, err := Decode(got)
		if err != nil || decoded != script {
			t.Errorf("Decode(Encode(%s)) = %q, %v", name, decoded, err)
		}
	}
}

func TestCommandFormsAndDecodeCommand(t *testing.T) {
	script := "[Console]::Out.Write('x')"
	long := Command("pwsh.exe", script)
	if !strings.HasPrefix(long, "pwsh.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand ") {
		t.Fatalf("Command = %q", long)
	}
	short := ShortCommand(script)
	if !strings.HasPrefix(short, "powershell -NoProfile -NonInteractive -EncodedCommand ") {
		t.Fatalf("ShortCommand = %q", short)
	}
	for _, command := range []string{long, short} {
		got, err := DecodeCommand(command)
		if err != nil || got != script {
			t.Fatalf("DecodeCommand(%q) = %q, %v", command, got, err)
		}
	}
	if _, err := DecodeCommand("systemctl status nginx"); err == nil {
		t.Fatal("DecodeCommand accepted a command without -EncodedCommand")
	}
	if _, err := Decode("AAA"); err == nil {
		t.Fatal("Decode accepted malformed base64")
	}
}
