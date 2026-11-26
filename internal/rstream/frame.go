package rstream

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// A frame is a 1-byte type, a 4-byte big-endian payload length and the payload. Carriers are byte streams, so a
// frame may span carrier messages and one carrier write may hold several frames.
const (
	frameOpen     byte = 1
	frameResume   byte = 2
	frameResumeOK byte = 3
	frameData     byte = 4
	frameAck      byte = 5
	frameFin      byte = 6
	frameReset    byte = 7
)

const (
	protocolVersion = 1
	headerSize      = 5
	sidSize         = 16
	keySize         = 32
	macSize         = sha256.Size
	// dataChunk bounds the bytes of one DATA frame and maxFramePayload any frame: well below the 1 MiB read limit
	// of the websocket hops on the relay.
	dataChunk       = 32 * 1024
	maxFramePayload = dataChunk + 64
	// batchLimit bounds the DATA bytes of one carrier write.
	batchLimit = 4 * dataChunk
	maxPurpose = 64
)

// Reset codes say why a stream or a carrier was refused.
const (
	ResetProtocol        uint16 = 1
	ResetUnknownStream   uint16 = 2
	ResetPurposeMismatch uint16 = 3
	ResetBadMAC          uint16 = 4
	ResetStaleResume     uint16 = 5
	ResetDuplicateStream uint16 = 6
	ResetTimeout         uint16 = 7
	ResetShutdown        uint16 = 8
	// ResetFinished answers a RESUME for a stream that completed: both directions were delivered.
	ResetFinished uint16 = 9
	// ResetClosed tells a peer that keeps sending that the application closed the stream.
	ResetClosed uint16 = 10
)

// ResetError is a RESET received from, or sent to, the peer.
type ResetError struct {
	Code    uint16
	Message string
}

func (e *ResetError) Error() string {
	return fmt.Sprintf("rstream: reset (code %d): %s", e.Code, e.Message)
}

var errProtocol = errors.New("rstream: protocol error")

type openFrame struct {
	sid     [sidSize]byte
	key     [keySize]byte
	window  uint32
	purpose string
}

type resumeFrame struct {
	sid  [sidSize]byte
	gen  uint64
	recv uint64
	mac  [macSize]byte
}

func appendHeader(dst []byte, kind byte, length int) []byte {
	dst = append(dst, kind)
	return binary.BigEndian.AppendUint32(dst, uint32(length))
}

func appendOpen(dst []byte, open openFrame) []byte {
	dst = appendHeader(dst, frameOpen, 1+sidSize+keySize+4+1+len(open.purpose))
	dst = append(dst, protocolVersion)
	dst = append(dst, open.sid[:]...)
	dst = append(dst, open.key[:]...)
	dst = binary.BigEndian.AppendUint32(dst, open.window)
	dst = append(dst, byte(len(open.purpose)))
	return append(dst, open.purpose...)
}

func parseOpen(payload []byte) (openFrame, error) {
	var open openFrame
	if len(payload) < 1+sidSize+keySize+4+1 || payload[0] != protocolVersion {
		return open, fmt.Errorf("%w: bad OPEN", errProtocol)
	}
	payload = payload[1:]
	copy(open.sid[:], payload[:sidSize])
	payload = payload[sidSize:]
	copy(open.key[:], payload[:keySize])
	payload = payload[keySize:]
	open.window = binary.BigEndian.Uint32(payload)
	payload = payload[4:]
	length := int(payload[0])
	if length > maxPurpose || len(payload) != 1+length {
		return open, fmt.Errorf("%w: bad OPEN purpose", errProtocol)
	}
	open.purpose = string(payload[1:])
	return open, nil
}

func resumeMAC(key [keySize]byte, sid [sidSize]byte, gen, recv uint64) [macSize]byte {
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte("nexterm-rstream-resume\x00"))
	mac.Write(sid[:])
	var numbers [16]byte
	binary.BigEndian.PutUint64(numbers[:8], gen)
	binary.BigEndian.PutUint64(numbers[8:], recv)
	mac.Write(numbers[:])
	var sum [macSize]byte
	copy(sum[:], mac.Sum(nil))
	return sum
}

func appendResume(dst []byte, resume resumeFrame) []byte {
	dst = appendHeader(dst, frameResume, sidSize+16+macSize)
	dst = append(dst, resume.sid[:]...)
	dst = binary.BigEndian.AppendUint64(dst, resume.gen)
	dst = binary.BigEndian.AppendUint64(dst, resume.recv)
	return append(dst, resume.mac[:]...)
}

func parseResume(payload []byte) (resumeFrame, error) {
	var resume resumeFrame
	if len(payload) != sidSize+16+macSize {
		return resume, fmt.Errorf("%w: bad RESUME", errProtocol)
	}
	copy(resume.sid[:], payload[:sidSize])
	resume.gen = binary.BigEndian.Uint64(payload[sidSize:])
	resume.recv = binary.BigEndian.Uint64(payload[sidSize+8:])
	copy(resume.mac[:], payload[sidSize+16:])
	return resume, nil
}

func appendOffset(dst []byte, kind byte, offset uint64) []byte {
	dst = appendHeader(dst, kind, 8)
	return binary.BigEndian.AppendUint64(dst, offset)
}

// appendAck reports the bytes the application consumed (the send window) and the stream position received (the
// delivery point, counting a received FIN as one position).
func appendAck(dst []byte, consumed, received uint64) []byte {
	dst = appendHeader(dst, frameAck, 16)
	dst = binary.BigEndian.AppendUint64(dst, consumed)
	return binary.BigEndian.AppendUint64(dst, received)
}

func parseAck(payload []byte) (consumed, received uint64, err error) {
	if len(payload) != 16 {
		return 0, 0, fmt.Errorf("%w: bad ACK", errProtocol)
	}
	return binary.BigEndian.Uint64(payload), binary.BigEndian.Uint64(payload[8:]), nil
}

func parseOffset(payload []byte) (uint64, error) {
	if len(payload) != 8 {
		return 0, fmt.Errorf("%w: bad offset frame", errProtocol)
	}
	return binary.BigEndian.Uint64(payload), nil
}

func appendDataHeader(dst []byte, offset uint64, length int) []byte {
	dst = appendHeader(dst, frameData, 8+length)
	return binary.BigEndian.AppendUint64(dst, offset)
}

func parseData(payload []byte) (uint64, []byte, error) {
	if len(payload) < 8 {
		return 0, nil, fmt.Errorf("%w: bad DATA", errProtocol)
	}
	return binary.BigEndian.Uint64(payload), payload[8:], nil
}

func appendReset(dst []byte, code uint16, message string) []byte {
	if len(message) > 512 {
		message = message[:512]
	}
	dst = appendHeader(dst, frameReset, 2+len(message))
	dst = binary.BigEndian.AppendUint16(dst, code)
	return append(dst, message...)
}

func parseReset(payload []byte) (*ResetError, error) {
	if len(payload) < 2 {
		return nil, fmt.Errorf("%w: bad RESET", errProtocol)
	}
	return &ResetError{Code: binary.BigEndian.Uint16(payload), Message: string(payload[2:])}, nil
}

// readFrame reads one frame; the payload is a fresh slice the caller owns.
func readFrame(reader *bufio.Reader) (byte, []byte, error) {
	var header [headerSize]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > maxFramePayload {
		return 0, nil, fmt.Errorf("%w: frame of %d bytes", errProtocol, length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return header[0], payload, nil
}
