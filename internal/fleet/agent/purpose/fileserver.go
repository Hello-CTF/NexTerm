package purpose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/fs/local"
)

// listChunkEntries bounds one FrameListed batch so a chunk stays well under
// the frame payload cap.
const listChunkEntries = 256

// FileServer serves device filesystem operations, one operation per bridge,
// delegating to the local filesystem implementation as the agent user.
type FileServer struct {
	bridge BridgeDialer
	files  *local.FileSystem
}

func NewFileServer(bridge BridgeDialer) *FileServer {
	return &FileServer{bridge: bridge, files: local.New()}
}

// Serve claims the bridge and serves its operation; it matches the agent
// runtime's purpose handler signature.
func (s *FileServer) Serve(ctx context.Context, bridgeID string) error {
	conn, err := s.bridge.DialBridge(ctx, bridgeID, deviceproto.PurposeFile)
	if err != nil {
		return err
	}
	return s.ServeConn(ctx, conn)
}

// ServeConn serves the single operation requested on conn and closes conn
// when done. Operation failures are reported to the client as error frames;
// the returned error only covers a broken bridge itself.
func (s *FileServer) ServeConn(ctx context.Context, conn net.Conn) error {
	defer func() { _ = conn.Close() }()
	kind, payload, err := deviceproto.ReadFrame(conn)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	switch kind {
	case deviceproto.FrameList:
		return s.serveList(ctx, conn, payload)
	case deviceproto.FrameReadFile:
		return s.serveReadFile(ctx, conn, payload)
	case deviceproto.FrameReadRange:
		return s.serveReadRange(ctx, conn, payload)
	case deviceproto.FrameWriteFile:
		return s.serveWriteFile(ctx, conn, payload)
	case deviceproto.FrameMkdir:
		return s.serveUnary(conn, payload, func(msg deviceproto.PathMsg) error {
			return s.files.Mkdir(ctx, msg.Path)
		})
	case deviceproto.FrameRename:
		var msg deviceproto.RenameMsg
		if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
			return deviceproto.WriteError(conn, err)
		}
		return s.finishUnary(conn, s.files.Rename(ctx, msg.From, msg.To))
	case deviceproto.FrameDelete:
		var msg deviceproto.DeleteMsg
		if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
			return deviceproto.WriteError(conn, err)
		}
		return s.finishUnary(conn, s.files.Delete(ctx, msg.Path, msg.Dir))
	case deviceproto.FrameChmod:
		var msg deviceproto.ChmodMsg
		if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
			return deviceproto.WriteError(conn, err)
		}
		return s.finishUnary(conn, s.files.Chmod(ctx, msg.Path, msg.Mode))
	case deviceproto.FrameChecksum:
		return s.serveChecksum(ctx, conn, payload)
	case deviceproto.FrameStat:
		return s.serveStat(ctx, conn, payload)
	case deviceproto.FrameOpenRead:
		return s.serveOpenRead(ctx, conn, payload)
	case deviceproto.FrameOpenWrite:
		return s.serveOpenWrite(ctx, conn, payload)
	default:
		return deviceproto.WriteError(conn, fmt.Errorf("%w: unexpected request frame %d", deviceproto.ErrProtocol, byte(kind)))
	}
}

func (s *FileServer) serveUnary(conn net.Conn, payload []byte, op func(deviceproto.PathMsg) error) error {
	var msg deviceproto.PathMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	return s.finishUnary(conn, op(msg))
}

// finishUnary reports an operation outcome as FrameOK or an error frame.
func (s *FileServer) finishUnary(conn net.Conn, err error) error {
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	return deviceproto.WriteFrame(conn, deviceproto.FrameOK, nil)
}

func (s *FileServer) serveList(ctx context.Context, conn net.Conn, payload []byte) error {
	var msg deviceproto.PathMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	entries, err := s.files.List(ctx, msg.Path)
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	for start := 0; start < len(entries); start += listChunkEntries {
		end := start + listChunkEntries
		if end > len(entries) {
			end = len(entries)
		}
		if err := deviceproto.WriteMessage(conn, deviceproto.FrameListed, deviceproto.ListChunk{Entries: entries[start:end]}); err != nil {
			return err
		}
	}
	return deviceproto.WriteFrame(conn, deviceproto.FrameEnd, nil)
}

func (s *FileServer) serveReadFile(ctx context.Context, conn net.Conn, payload []byte) error {
	var msg deviceproto.ReadMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	if msg.MaxBytes <= 0 || msg.MaxBytes > deviceproto.MaxReadBytes {
		return deviceproto.WriteError(conn, fmt.Errorf("%w: maximum read size must be in (0, %d]", deviceproto.ErrInvalid, deviceproto.MaxReadBytes))
	}
	data, err := s.files.ReadFile(ctx, msg.Path, msg.MaxBytes)
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	return writeFileData(conn, int64(len(data)), data)
}

func (s *FileServer) serveReadRange(ctx context.Context, conn net.Conn, payload []byte) error {
	var msg deviceproto.RangeMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	if msg.Offset < 0 {
		return deviceproto.WriteError(conn, fmt.Errorf("%w: read offset must not be negative", deviceproto.ErrInvalid))
	}
	if msg.MaxBytes <= 0 || msg.MaxBytes > deviceproto.MaxReadBytes {
		return deviceproto.WriteError(conn, fmt.Errorf("%w: maximum read size must be in (0, %d]", deviceproto.ErrInvalid, deviceproto.MaxReadBytes))
	}
	file, err := os.Open(s.files.LocalPath(msg.Path))
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	defer func() { _ = file.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = file.Close() })
	defer stop()
	if _, err := file.Seek(msg.Offset, io.SeekStart); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	info, err := file.Stat()
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	if msg.Offset >= info.Size() {
		return writeFileData(conn, info.Size(), nil)
	}
	data, err := io.ReadAll(io.LimitReader(file, msg.MaxBytes))
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	return writeFileData(conn, info.Size(), data)
}

func (s *FileServer) serveWriteFile(ctx context.Context, conn net.Conn, payload []byte) error {
	var msg deviceproto.WriteMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	if msg.Size < 0 || msg.Size > deviceproto.MaxWriteFileSize {
		return deviceproto.WriteError(conn, fmt.Errorf("%w: declared write size %d out of bounds", deviceproto.ErrInvalid, msg.Size))
	}
	data, err := readDeclaredData(conn, msg.Size)
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	return s.finishUnary(conn, s.files.WriteFile(ctx, msg.Path, data, msg.Backup))
}

// readDeclaredData collects the data frame stream up to a FrameEnd, refusing
// anything past the declared size.
func readDeclaredData(conn net.Conn, declared int64) ([]byte, error) {
	var buffer bytes.Buffer
	for {
		kind, payload, err := deviceproto.ReadFrame(conn)
		if err != nil {
			return nil, err
		}
		switch kind {
		case deviceproto.FrameData:
			if int64(buffer.Len())+int64(len(payload)) > declared {
				return nil, fmt.Errorf("%w: write payload exceeds declared %d bytes", deviceproto.ErrProtocol, declared)
			}
			buffer.Write(payload)
		case deviceproto.FrameEnd:
			if int64(buffer.Len()) != declared {
				return nil, fmt.Errorf("%w: write payload of %d bytes mismatches declared %d", deviceproto.ErrProtocol, buffer.Len(), declared)
			}
			return buffer.Bytes(), nil
		default:
			return nil, fmt.Errorf("%w: unexpected write stream frame %d", deviceproto.ErrProtocol, byte(kind))
		}
	}
}

func (s *FileServer) serveChecksum(ctx context.Context, conn net.Conn, payload []byte) error {
	var msg deviceproto.ChecksumMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	sum, err := s.files.Checksum(ctx, msg.Path, msg.Algorithm)
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	return deviceproto.WriteMessage(conn, deviceproto.FrameChecksumReply, deviceproto.ChecksumReply{Sum: sum})
}

func (s *FileServer) serveStat(ctx context.Context, conn net.Conn, payload []byte) error {
	var msg deviceproto.PathMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	size, err := s.files.Size(ctx, msg.Path)
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	return deviceproto.WriteMessage(conn, deviceproto.FrameStatReply, deviceproto.StatReply{Size: size})
}

func (s *FileServer) serveOpenRead(ctx context.Context, conn net.Conn, payload []byte) error {
	var msg deviceproto.PathMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	reader, err := s.files.OpenRead(ctx, msg.Path)
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	defer func() { _ = reader.Close() }()
	if err := deviceproto.WriteMessage(conn, deviceproto.FrameOpened, deviceproto.OpenedMsg{Size: reader.Size()}); err != nil {
		return err
	}
	buffer := make([]byte, deviceproto.DataChunkSize)
	for {
		count, readErr := reader.Read(buffer)
		if count > 0 {
			if err := deviceproto.WriteFrame(conn, deviceproto.FrameData, buffer[:count]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return deviceproto.WriteFrame(conn, deviceproto.FrameEnd, nil)
		}
		if readErr != nil {
			return deviceproto.WriteError(conn, readErr)
		}
	}
}

func (s *FileServer) serveOpenWrite(ctx context.Context, conn net.Conn, payload []byte) error {
	var msg deviceproto.OpenWriteMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	writer, err := s.files.OpenWrite(ctx, msg.Path, msg.Append)
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	if err := deviceproto.WriteMessage(conn, deviceproto.FrameOpened, deviceproto.OpenedMsg{}); err != nil {
		_ = writer.Close()
		return err
	}
	for {
		kind, payload, err := deviceproto.ReadFrame(conn)
		if err != nil {
			_ = writer.Close()
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch kind {
		case deviceproto.FrameData:
			if _, err := writer.Write(payload); err != nil {
				_ = writer.Close()
				return deviceproto.WriteError(conn, err)
			}
		case deviceproto.FrameSync:
			if err := writer.Sync(); err != nil {
				_ = writer.Close()
				return deviceproto.WriteError(conn, err)
			}
			if err := deviceproto.WriteFrame(conn, deviceproto.FrameOK, nil); err != nil {
				_ = writer.Close()
				return err
			}
		case deviceproto.FrameClose:
			err := writer.Close()
			if err != nil {
				return deviceproto.WriteError(conn, err)
			}
			return deviceproto.WriteFrame(conn, deviceproto.FrameOK, nil)
		default:
			_ = writer.Close()
			return deviceproto.WriteError(conn, fmt.Errorf("%w: unexpected write stream frame %d", deviceproto.ErrProtocol, byte(kind)))
		}
	}
}

// writeFileData streams a read reply: the header carries the total size,
// chunks carry the requested window.
func writeFileData(conn net.Conn, size int64, data []byte) error {
	if err := deviceproto.WriteMessage(conn, deviceproto.FrameFileData, deviceproto.FileDataMsg{Size: size}); err != nil {
		return err
	}
	for len(data) > 0 {
		chunk := data
		if len(chunk) > deviceproto.DataChunkSize {
			chunk = chunk[:deviceproto.DataChunkSize]
		}
		if err := deviceproto.WriteFrame(conn, deviceproto.FrameData, chunk); err != nil {
			return err
		}
		data = data[len(chunk):]
	}
	return deviceproto.WriteFrame(conn, deviceproto.FrameEnd, nil)
}
