package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// parsePlist decodes an XML or binary property list into dictionaries
// (map[string]any), arrays ([]any), strings, bools, int64, float64, dates
// (time.Time) and data ([]byte).
func parsePlist(data []byte) (any, error) {
	if bytes.HasPrefix(data, []byte("bplist00")) {
		return parseBinaryPlist(data)
	}
	return parseXMLPlist(data)
}

func parseXMLPlist(data []byte) (any, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid XML plist: %v", err)
		}
		if start, ok := token.(xml.StartElement); ok {
			if start.Name.Local != "plist" {
				return nil, fmt.Errorf("invalid XML plist: unexpected root <%s>", start.Name.Local)
			}
			value, err := nextXMLValue(decoder, 0)
			if err != nil {
				return nil, err
			}
			if err := expectXMLEnd(decoder, "plist"); err != nil {
				return nil, err
			}
			for {
				token, err := decoder.Token()
				if errors.Is(err, io.EOF) {
					return value, nil
				}
				if err != nil {
					return nil, fmt.Errorf("invalid XML plist: %v", err)
				}
				if _, ok := token.(xml.StartElement); ok {
					return nil, errors.New("invalid XML plist: content after </plist>")
				}
			}
		}
	}
}

// nextXMLValue reads the next value element, skipping whitespace and comments.
func nextXMLValue(decoder *xml.Decoder, depth int) (any, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid XML plist: %v", err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			return xmlValue(decoder, element, depth)
		case xml.EndElement:
			return nil, errEndOfContainer
		case xml.CharData:
			if strings.TrimSpace(string(element)) != "" {
				return nil, errors.New("invalid XML plist: unexpected text")
			}
		}
	}
}

var errEndOfContainer = errors.New("end of container")

func xmlValue(decoder *xml.Decoder, start xml.StartElement, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("invalid XML plist: nesting is too deep")
	}
	name := start.Name.Local
	switch name {
	case "dict":
		dict := map[string]any{}
		for {
			key, err := nextXMLValue(decoder, depth+1)
			if errors.Is(err, errEndOfContainer) {
				return dict, nil
			}
			if err != nil {
				return nil, err
			}
			keyName, ok := key.(plistKey)
			if !ok {
				return nil, errors.New("invalid XML plist: dict entry without <key>")
			}
			value, err := nextXMLValue(decoder, depth+1)
			if err != nil {
				if errors.Is(err, errEndOfContainer) {
					return nil, errors.New("invalid XML plist: <key> without a value")
				}
				return nil, err
			}
			if _, isKey := value.(plistKey); isKey {
				return nil, errors.New("invalid XML plist: <key> without a value")
			}
			dict[string(keyName)] = value
		}
	case "array":
		array := []any{}
		for {
			value, err := nextXMLValue(decoder, depth+1)
			if errors.Is(err, errEndOfContainer) {
				return array, nil
			}
			if err != nil {
				return nil, err
			}
			if _, isKey := value.(plistKey); isKey {
				return nil, errors.New("invalid XML plist: <key> inside <array>")
			}
			array = append(array, value)
		}
	case "true", "false":
		if err := expectXMLEnd(decoder, name); err != nil {
			return nil, err
		}
		return name == "true", nil
	}
	text, err := xmlText(decoder, name)
	if err != nil {
		return nil, err
	}
	switch name {
	case "key":
		return plistKey(text), nil
	case "string":
		return text, nil
	case "integer":
		value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid XML plist integer %q", text)
		}
		return value, nil
	case "real":
		value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid XML plist real %q", text)
		}
		return value, nil
	case "date":
		value, err := time.Parse(time.RFC3339, strings.TrimSpace(text))
		if err != nil {
			return nil, fmt.Errorf("invalid XML plist date %q", text)
		}
		return value, nil
	case "data":
		value, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(text), ""))
		if err != nil {
			return nil, errors.New("invalid XML plist data")
		}
		return value, nil
	}
	return nil, fmt.Errorf("invalid XML plist: unknown element <%s>", name)
}

type plistKey string

func xmlText(decoder *xml.Decoder, name string) (string, error) {
	var text strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", fmt.Errorf("invalid XML plist: %v", err)
		}
		switch element := token.(type) {
		case xml.CharData:
			text.Write(element)
		case xml.EndElement:
			if element.Name.Local != name {
				return "", fmt.Errorf("invalid XML plist: mismatched </%s>", element.Name.Local)
			}
			return text.String(), nil
		case xml.StartElement:
			return "", fmt.Errorf("invalid XML plist: unexpected <%s> inside <%s>", element.Name.Local, name)
		}
	}
}

func expectXMLEnd(decoder *xml.Decoder, name string) error {
	text, err := xmlText(decoder, name)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) != "" {
		return fmt.Errorf("invalid XML plist: unexpected text in <%s>", name)
	}
	return nil
}

type binaryPlist struct {
	data          []byte
	offsets       []uint64
	objectRefSize int
	visiting      map[uint64]bool
}

func parseBinaryPlist(data []byte) (any, error) {
	if len(data) < 8+32 {
		return nil, errors.New("invalid binary plist: truncated")
	}
	trailer := data[len(data)-32:]
	offsetSize := int(trailer[6])
	refSize := int(trailer[7])
	count := binary.BigEndian.Uint64(trailer[8:])
	top := binary.BigEndian.Uint64(trailer[16:])
	tableOffset := binary.BigEndian.Uint64(trailer[24:])
	if offsetSize < 1 || offsetSize > 8 || refSize < 1 || refSize > 8 || count == 0 || top >= count {
		return nil, errors.New("invalid binary plist trailer")
	}
	tableEnd := uint64(len(data) - 32)
	if tableOffset < 8 || tableOffset > tableEnd || count > (tableEnd-tableOffset)/uint64(offsetSize) {
		return nil, errors.New("invalid binary plist offset table")
	}
	parser := &binaryPlist{data: data[:tableOffset], objectRefSize: refSize, visiting: map[uint64]bool{}}
	parser.offsets = make([]uint64, count)
	for index := range parser.offsets {
		start := tableOffset + uint64(index*offsetSize)
		offset := readBigEndian(data[start : start+uint64(offsetSize)])
		if offset < 8 || offset >= tableOffset {
			return nil, errors.New("invalid binary plist object offset")
		}
		parser.offsets[index] = offset
	}
	return parser.object(top, 0)
}

func readBigEndian(value []byte) uint64 {
	var result uint64
	for _, b := range value {
		result = result<<8 | uint64(b)
	}
	return result
}

func (p *binaryPlist) bytesAt(offset, length uint64) ([]byte, error) {
	if offset > uint64(len(p.data)) || length > uint64(len(p.data))-offset {
		return nil, errors.New("invalid binary plist: object extends past the object table")
	}
	return p.data[offset : offset+length], nil
}

// length decodes a marker's length nibble, following an int object when the
// nibble is 0xF, and returns the length and the offset of the payload.
func (p *binaryPlist) length(offset uint64, nibble byte) (uint64, uint64, error) {
	if nibble != 0xf {
		return uint64(nibble), offset + 1, nil
	}
	marker, err := p.bytesAt(offset+1, 1)
	if err != nil {
		return 0, 0, err
	}
	if marker[0]>>4 != 0x1 {
		return 0, 0, errors.New("invalid binary plist length marker")
	}
	size := uint64(1) << (marker[0] & 0xf)
	if size > 8 {
		return 0, 0, errors.New("invalid binary plist length size")
	}
	value, err := p.bytesAt(offset+2, size)
	if err != nil {
		return 0, 0, err
	}
	return readBigEndian(value), offset + 2 + size, nil
}

func (p *binaryPlist) refs(offset, count uint64) ([]uint64, error) {
	if count > uint64(len(p.data)) {
		return nil, errors.New("invalid binary plist container size")
	}
	raw, err := p.bytesAt(offset, count*uint64(p.objectRefSize))
	if err != nil {
		return nil, err
	}
	refs := make([]uint64, count)
	for index := range refs {
		refs[index] = readBigEndian(raw[index*p.objectRefSize : (index+1)*p.objectRefSize])
	}
	return refs, nil
}

func (p *binaryPlist) object(ref uint64, depth int) (any, error) {
	if ref >= uint64(len(p.offsets)) {
		return nil, errors.New("invalid binary plist object reference")
	}
	if depth > 64 || p.visiting[ref] {
		return nil, errors.New("invalid binary plist: recursive or too deeply nested object")
	}
	p.visiting[ref] = true
	defer delete(p.visiting, ref)
	offset := p.offsets[ref]
	marker, err := p.bytesAt(offset, 1)
	if err != nil {
		return nil, err
	}
	kind, nibble := marker[0]>>4, marker[0]&0xf
	switch kind {
	case 0x0:
		switch marker[0] {
		case 0x08:
			return false, nil
		case 0x09:
			return true, nil
		}
		return nil, fmt.Errorf("unsupported binary plist marker 0x%02x", marker[0])
	case 0x1:
		size := uint64(1) << nibble
		if size > 8 {
			return nil, errors.New("unsupported binary plist integer size")
		}
		value, err := p.bytesAt(offset+1, size)
		if err != nil {
			return nil, err
		}
		return int64(readBigEndian(value)), nil
	case 0x2:
		value, err := p.bytesAt(offset+1, uint64(1)<<nibble)
		if err != nil {
			return nil, err
		}
		switch len(value) {
		case 4:
			return float64(math.Float32frombits(binary.BigEndian.Uint32(value))), nil
		case 8:
			return math.Float64frombits(binary.BigEndian.Uint64(value)), nil
		}
		return nil, errors.New("unsupported binary plist real size")
	case 0x3:
		value, err := p.bytesAt(offset+1, 8)
		if err != nil {
			return nil, err
		}
		seconds := math.Float64frombits(binary.BigEndian.Uint64(value))
		return time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(seconds * float64(time.Second))), nil
	case 0x4, 0x5, 0x6:
		count, start, err := p.length(offset, nibble)
		if err != nil {
			return nil, err
		}
		width := uint64(1)
		if kind == 0x6 {
			width = 2
		}
		if count > uint64(len(p.data)) {
			return nil, errors.New("invalid binary plist string size")
		}
		raw, err := p.bytesAt(start, count*width)
		if err != nil {
			return nil, err
		}
		switch kind {
		case 0x4:
			return append([]byte(nil), raw...), nil
		case 0x5:
			if !isASCII(raw) {
				return nil, errors.New("invalid binary plist ASCII string")
			}
			return string(raw), nil
		}
		units := make([]uint16, count)
		for index := range units {
			units[index] = binary.BigEndian.Uint16(raw[index*2:])
		}
		return string(utf16.Decode(units)), nil
	case 0xa:
		count, start, err := p.length(offset, nibble)
		if err != nil {
			return nil, err
		}
		refs, err := p.refs(start, count)
		if err != nil {
			return nil, err
		}
		array := make([]any, 0, len(refs))
		for _, child := range refs {
			value, err := p.object(child, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		return array, nil
	case 0xd:
		count, start, err := p.length(offset, nibble)
		if err != nil {
			return nil, err
		}
		refs, err := p.refs(start, count*2)
		if err != nil {
			return nil, err
		}
		dict := make(map[string]any, count)
		for index := uint64(0); index < count; index++ {
			key, err := p.object(refs[index], depth+1)
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, errors.New("invalid binary plist dictionary key")
			}
			value, err := p.object(refs[count+index], depth+1)
			if err != nil {
				return nil, err
			}
			dict[name] = value
		}
		return dict, nil
	}
	return nil, fmt.Errorf("unsupported binary plist marker 0x%02x", marker[0])
}
