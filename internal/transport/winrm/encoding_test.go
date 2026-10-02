package winrm

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestDecodeBytesUTF8AndGB18030(t *testing.T) {
	if got := decodeBytes([]byte("中文 UTF-8")); got != "中文 UTF-8" {
		t.Fatalf("UTF-8 decode = %q", got)
	}
	raw, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte("中文目录𠀀"))
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeBytes(raw); got != "中文目录𠀀" {
		t.Fatalf("GB18030 decode = %q", got)
	}
}

func TestDecodeBytesInvalidInputIsValidUTF8(t *testing.T) {
	got := decodeBytes([]byte{0xff, 0xfe, 0xff})
	if !strings.Contains(got, "\uFFFD") {
		t.Fatalf("invalid input decode = %q", got)
	}
}
