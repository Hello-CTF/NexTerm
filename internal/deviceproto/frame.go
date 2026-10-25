package deviceproto

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"sync"
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
	FrameListen
	FrameListenAck
	FrameInbound
	FrameAccept
	FrameRoots
	FrameRootsReply
)

const (
	frameHeaderSize = 5
	// pooledFrameSize is the largest frame (header included) served from the buffer pool; larger frames are allocated
	// exactly once instead of pinning big buffers in the pool.
	pooledFrameSize = 64*1024 + frameHeaderSize
)

var frameBufferPool = sync.Pool{New: func() any {
	buffer := make([]byte, pooledFrameSize)
	return &buffer
}}

var frameHeaderPool = sync.Pool{New: func() any {
	header := make([]byte, frameHeaderSize)
	return &header
}}

// WriteFrame writes one frame with a single Write call (header and payload together), so a message-oriented
// transport such as a WebSocket carries the frame in one message. The writer must not retain the slice after
// Write returns, which io.Writer already requires; the buffer is pooled and reused.
func WriteFrame(writer io.Writer, kind FrameType, payload []byte) error {
	if len(payload) > MaxFramePayload {
		return fmt.Errorf("%w: frame payload of %d bytes exceeds limit", ErrProtocol, len(payload))
	}
	total := frameHeaderSize + len(payload)
	var frame []byte
	if total <= pooledFrameSize {
		pooled := frameBufferPool.Get().(*[]byte)
		defer frameBufferPool.Put(pooled)
		frame = (*pooled)[:total]
	} else {
		frame = make([]byte, total)
	}
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	frame[4] = byte(kind)
	copy(frame[frameHeaderSize:], payload)
	for len(frame) > 0 {
		count, err := writer.Write(frame)
		if err != nil {
			return err
		}
		frame = frame[count:]
	}
	return nil
}

// ReadFrame reads one frame. The returned payload is freshly allocated and owned by the caller; it never aliases
// pooled memory, so callers may retain it.
func ReadFrame(reader io.Reader) (FrameType, []byte, error) {
	pooled := frameHeaderPool.Get().(*[]byte)
	defer frameHeaderPool.Put(pooled)
	header := *pooled
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header)
	kind := FrameType(header[4])
	if length > MaxFramePayload {
		return 0, nil, fmt.Errorf("%w: frame payload of %d bytes exceeds limit", ErrProtocol, length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	return kind, payload, nil
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
