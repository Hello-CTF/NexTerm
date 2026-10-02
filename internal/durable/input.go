package durable

import (
	"fmt"
	"unicode/utf8"
)

const (
	maxLiteralInput = 2048
	maxControlInput = 256
)

type inputOperation struct {
	literal bool
	data    []byte
	size    int
}

func makeInputOperations(input []byte) ([]inputOperation, error) {
	operations := make([]inputOperation, 0, 2)
	for offset := 0; offset < len(input); {
		if isControlByte(input[offset]) {
			start := offset
			for offset < len(input) && isControlByte(input[offset]) && offset-start < maxControlInput {
				offset++
			}
			operations = append(operations, inputOperation{data: input[start:offset], size: offset - start})
			continue
		}
		start := offset
		for offset < len(input) && !isControlByte(input[offset]) {
			_, size := utf8.DecodeRune(input[offset:])
			if size == 1 && input[offset] >= utf8.RuneSelf {
				return nil, fmt.Errorf("%w: invalid UTF-8 at byte %d", ErrInvalidInput, offset)
			}
			if offset-start+size > maxLiteralInput {
				break
			}
			offset += size
		}
		operations = append(operations, inputOperation{literal: true, data: input[start:offset], size: offset - start})
	}
	return operations, nil
}

func isControlByte(value byte) bool { return value < 0x20 || value == 0x7f }

func hexByte(value byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[value>>4], digits[value&0x0f]})
}
