package terminal

import (
	"fmt"
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
)

type Encoding int

const (
	UTF8 Encoding = iota
	GBK
	GB18030
	Big5
	Latin1
)

func (e Encoding) String() string {
	switch e {
	case UTF8:
		return "utf8"
	case GBK:
		return "gbk"
	case GB18030:
		return "gb18030"
	case Big5:
		return "big5"
	case Latin1:
		return "latin1"
	default:
		return fmt.Sprintf("encoding(%d)", int(e))
	}
}

func ParseEncoding(s string) (Encoding, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "utf-8", "utf8":
		return UTF8, nil
	case "gbk":
		return GBK, nil
	case "gb18030":
		return GB18030, nil
	case "big5":
		return Big5, nil
	case "latin1", "iso-8859-1":
		return Latin1, nil
	default:
		return UTF8, fmt.Errorf("unknown terminal encoding %q", s)
	}
}

func (e Encoding) MarshalText() ([]byte, error) { return []byte(e.String()), nil }

func (e *Encoding) UnmarshalText(text []byte) error {
	parsed, err := ParseEncoding(string(text))
	if err != nil {
		return err
	}
	*e = parsed
	return nil
}

func (e Encoding) decoder() *encoding.Decoder {
	switch e {
	case GBK:
		return simplifiedchinese.GBK.NewDecoder()
	case GB18030:
		return simplifiedchinese.GB18030.NewDecoder()
	case Big5:
		return traditionalchinese.Big5.NewDecoder()
	case Latin1:
		return charmap.Windows1252.NewDecoder()
	default:
		return unicode.UTF8.NewDecoder()
	}
}
