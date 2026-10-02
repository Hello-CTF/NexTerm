package winrm

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func decodeBytes(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
	if err == nil {
		return string(decoded)
	}
	return strings.ToValidUTF8(string(raw), "\uFFFD")
}
