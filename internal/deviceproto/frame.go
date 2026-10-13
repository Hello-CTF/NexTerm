package deviceproto

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

type FrameType byte

const (
	FrameError FrameType = 1 + iota
	FrameList
	FrameListed
	FrameReadFile
	FrameReadRange
	FrameFileData
	FrameWriteFile
	FrameMkdir
	FrameRename
	FrameDelete
	FrameChmod
	FrameChecksum
	FrameChecksumReply
	FrameStat
	FrameStatReply
	FrameOK
	FrameOpenRead
	FrameOpenWrite
	FrameOpened
	FrameData
	FrameEnd
	FrameSync
	FrameClose
	FrameDial
	FrameDialed
	FrameDialData
	FrameDialEOF
)

func WriteFrame(writer io.Writer, kind FrameType, payload []byte) error {
	if len(payload) > MaxFramePayload {
		return fmt.Errorf("%w: frame payload of %d bytes exceeds limit", ErrProtocol, len(payload))
	}
	header := make([]byte, 5)
	binary.BigEndian.PutUint32(header, uint32(len(payload)))
	header[4] = byte(kind)
	frame := append(header, payload...)
	for len(frame) > 0 {
		count, err := writer.Write(frame)
		if err != nil {
			return err
		}
		frame = frame[count:]
	}
	return nil
}

func ReadFrame(reader io.Reader) (FrameType, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header)
	if length > MaxFramePayload {
		return 0, nil, fmt.Errorf("%w: frame payload of %d bytes exceeds limit", ErrProtocol, length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	return FrameType(header[4]), payload, nil
}

func WriteMessage(writer io.Writer, kind FrameType, message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("%w: encode frame: %v", ErrProtocol, err)
	}
	return WriteFrame(writer, kind, payload)
}

func UnmarshalPayload(payload []byte, message any) error {
	if err := json.Unmarshal(payload, message); err != nil {
		return fmt.Errorf("%w: malformed frame payload: %v", ErrProtocol, err)
	}
	return nil
}
