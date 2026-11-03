package agent

import (
	"bytes"
	"os"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
)

func TestTypographicQuotesCannotCloseLiteral_fa(t *testing.T) {
	value := "x\u2019; Remove-Item -Recurse C:\\ ; \u2018y\u201A\u201B'"
	parsed, ok := psquote.Unquote(psQuote(value))
	if !ok || parsed != value {
		t.Fatalf("value %q parsed as %q ok=%v", value, parsed, ok)
	}
}

func TestReplaceScriptFileStartsWithUTF8BOM(t *testing.T) {
	path, err := writeReplaceScriptFile(t.TempDir(), "$x = '’'\n")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) || !bytes.HasSuffix(data, []byte("'’'\n")) {
		t.Fatalf("unexpected script bytes %q", data)
	}
}
