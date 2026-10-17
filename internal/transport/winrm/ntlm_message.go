package winrm

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

const (
	mimeBoundary         = "--Encrypted Boundary"
	maxEncryptedPartSize = 16384
)

var (
	mimeBoundaryBytes = []byte(mimeBoundary)
	mimePartSeparator = append(append([]byte(nil), mimeBoundaryBytes...), []byte("\r\n")...)
)

type messageProtector interface {
	Wrap([]byte) ([]byte, error)
	Unwrap(data []byte, expectedLength int) ([]byte, error)
}

type winRMMessageEncryption struct {
	protocolString []byte
	protector      messageProtector
	multipart      bool
}

func newWinRMMessageEncryption(protocol string, protector messageProtector) (*winRMMessageEncryption, error) {
	if protector == nil {
		return nil, errors.New("message encryption protector is nil")
	}
	var protocolString string
	switch protocol {
	case "ntlm":
		protocolString = "application/HTTP-SPNEGO-session-encrypted"
	default:
		return nil, fmt.Errorf("message encryption for protocol %q not supported", protocol)
	}
	return &winRMMessageEncryption{protocolString: []byte(protocolString), protector: protector}, nil
}

func (e *winRMMessageEncryption) contentType() string {
	mediaType := "multipart/encrypted"
	if e.multipart {
		mediaType = "multipart/x-multi-encrypted"
	}
	return fmt.Sprintf(`%s;protocol="%s";boundary="Encrypted Boundary"`, mediaType, e.protocolString)
}

func (e *winRMMessageEncryption) encrypt(message []byte) ([]byte, error) {
	chunks := chunkEncryptedMessage(message, maxEncryptedPartSize)
	e.multipart = len(chunks) > 1
	var payload bytes.Buffer
	payload.Grow(len(message) + 256*len(chunks))
	for _, chunk := range chunks {
		encryptedStream, err := e.protector.Wrap(chunk)
		if err != nil {
			return nil, fmt.Errorf("wrap WinRM message: %w", err)
		}
		payload.Write(mimeBoundaryBytes)
		payload.WriteString("\r\n")
		fmt.Fprintf(&payload, "\tContent-Type: %s\r\n", e.protocolString)
		fmt.Fprintf(&payload, "\tOriginalContent: type=application/soap+xml;charset=UTF-8;Length=%d\r\n", len(chunk))
		payload.Write(mimeBoundaryBytes)
		payload.WriteString("\r\n")
		payload.WriteString("\tContent-Type: application/octet-stream\r\n")
		payload.Write(encryptedStream)
	}
	payload.Write(mimeBoundaryBytes)
	payload.WriteString("--\r\n")
	return payload.Bytes(), nil
}

func chunkEncryptedMessage(message []byte, size int) [][]byte {
	if len(message) <= size {
		return [][]byte{message}
	}
	chunks := make([][]byte, 0, (len(message)+size-1)/size)
	for offset := 0; offset < len(message); offset += size {
		end := min(offset+size, len(message))
		chunks = append(chunks, message[offset:end])
	}
	return chunks
}

func (e *winRMMessageEncryption) decryptResponse(response *http.Response) ([]byte, error) {
	if response == nil {
		return nil, errors.New("encrypted response is nil")
	}
	if response.Body == nil {
		return nil, errors.New("encrypted response body is nil")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read encrypted response body: %w", err)
	}
	return e.decrypt(body)
}

func (e *winRMMessageEncryption) decrypt(body []byte) ([]byte, error) {
	parts := bytes.Split(body, mimePartSeparator)
	filtered := parts[:0]
	for _, part := range parts {
		if len(part) > 0 {
			filtered = append(filtered, part)
		}
	}
	parts = filtered
	if len(parts) == 0 || len(parts)%2 != 0 {
		return nil, fmt.Errorf("invalid encrypted multipart body: expected header/payload pairs, got %d parts", len(parts))
	}
	var message bytes.Buffer
	for i := 0; i < len(parts); i += 2 {
		header := parts[i]
		payload := parts[i+1]
		expectedLength, err := encryptedPartOriginalLength(header)
		if err != nil {
			return nil, fmt.Errorf("encrypted MIME part %d: %w", i/2, err)
		}
		if bytes.HasSuffix(payload, []byte(mimeBoundary+"--\r\n")) {
			payload = payload[:len(payload)-len(mimeBoundary)-4]
		}
		const octetStreamHeader = "\tContent-Type: application/octet-stream\r\n"
		if !bytes.HasPrefix(payload, []byte(octetStreamHeader)) {
			return nil, fmt.Errorf("encrypted MIME part %d is missing the octet-stream header", i/2)
		}
		decrypted, err := e.protector.Unwrap(payload[len(octetStreamHeader):], expectedLength)
		if err != nil {
			return nil, fmt.Errorf("unwrap encrypted MIME part %d: %w", i/2, err)
		}
		if len(decrypted) != expectedLength {
			return nil, fmt.Errorf("encrypted MIME part %d length mismatch: expected %d, got %d", i/2, expectedLength, len(decrypted))
		}
		message.Write(decrypted)
	}
	return message.Bytes(), nil
}

func encryptedPartOriginalLength(header []byte) (int, error) {
	const marker = "Length="
	index := bytes.Index(header, []byte(marker))
	if index < 0 {
		return 0, errors.New("missing OriginalContent length")
	}
	value := header[index+len(marker):]
	if end := bytes.IndexAny(value, "\r\n \t"); end >= 0 {
		value = value[:end]
	}
	if len(value) == 0 {
		return 0, errors.New("empty OriginalContent length")
	}
	length, err := strconv.Atoi(string(value))
	if err != nil || length < 0 {
		return 0, fmt.Errorf("invalid OriginalContent length %q", value)
	}
	return length, nil
}
