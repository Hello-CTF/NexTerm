package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func (s *Server) serveWriteVersion(ctx context.Context, conn net.Conn, payload []byte) error {
	var msg deviceproto.WriteVersionMsg
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
	return s.finishUnary(conn, s.files.WriteFileVersion(ctx, msg.Path, data, msg.Backup, msg.Expected))
}

func (s *Server) serveStatInfo(conn net.Conn, payload []byte) error {
	var msg deviceproto.PathMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	info, err := os.Stat(s.files.LocalPath(msg.Path))
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	return deviceproto.WriteMessage(conn, deviceproto.FrameStatInfoReply, deviceproto.StatInfoReply{
		Size: info.Size(), Mode: info.Mode(), ModTime: info.ModTime(), Kind: base.TargetKindOf(info.Mode()),
	})
}

// serveWatch holds a watch for as long as the client keeps the stream open,
// replacing its directories on every FrameWatchSet.
func (s *Server) serveWatch(ctx context.Context, conn net.Conn, payload []byte) error {
	var msg deviceproto.WatchMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var writeMu sync.Mutex
	var writeErr error
	send := func(event base.WatchEvent) {
		writeMu.Lock()
		defer writeMu.Unlock()
		if writeErr != nil {
			return
		}
		if writeErr = deviceproto.WriteMessage(conn, deviceproto.FrameWatchEvent, event); writeErr != nil {
			cancel()
		}
	}
	failed := make(chan error, 1)
	handle, err := s.files.Watch(ctx, send, func(err error) {
		failed <- err
	})
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	defer func() { _ = handle.Close() }()
	writeMu.Lock()
	writeErr = deviceproto.WriteFrame(conn, deviceproto.FrameOK, nil)
	err = writeErr
	writeMu.Unlock()
	if err != nil {
		return err
	}
	if err := handle.Set(msg.Dirs); err != nil {
		return err
	}
	requests := make(chan error, 1)
	go func() {
		for {
			kind, payload, err := deviceproto.ReadFrame(conn)
			if err != nil {
				requests <- err
				return
			}
			if kind != deviceproto.FrameWatchSet {
				requests <- fmt.Errorf("%w: unexpected watch frame %d", deviceproto.ErrProtocol, byte(kind))
				return
			}
			var update deviceproto.WatchMsg
			if err := deviceproto.UnmarshalPayload(payload, &update); err != nil {
				requests <- err
				return
			}
			if err := handle.Set(update.Dirs); err != nil {
				requests <- err
				return
			}
		}
	}()
	select {
	case err := <-requests:
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	case err := <-failed:
		if err == nil || ctx.Err() != nil {
			return nil
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		if writeErr != nil {
			return writeErr
		}
		writeErr = deviceproto.WriteError(conn, err)
		return writeErr
	}
}

// servePack streams a directory as a gzipped tar: FrameOpened, data frames,
// then FrameEnd, or an error frame when packing fails midway.
func (s *Server) servePack(ctx context.Context, conn net.Conn, payload []byte) error {
	var msg deviceproto.PathMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	info, err := os.Stat(s.files.LocalPath(msg.Path))
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	if !info.IsDir() {
		return deviceproto.WriteError(conn, fmt.Errorf("%w: pack source is not a directory", deviceproto.ErrInvalid))
	}
	if err := deviceproto.WriteMessage(conn, deviceproto.FrameOpened, deviceproto.OpenedMsg{Size: -1}); err != nil {
		return err
	}
	out := &frameWriter{conn: conn}
	if err := s.files.WritePack(ctx, msg.Path, out); err != nil {
		if out.err != nil {
			return out.err
		}
		return deviceproto.WriteError(conn, err)
	}
	if err := out.flush(); err != nil {
		return err
	}
	return deviceproto.WriteFrame(conn, deviceproto.FrameEnd, nil)
}

func (s *Server) serveExtract(ctx context.Context, conn net.Conn, payload []byte) error {
	var msg deviceproto.PathMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	dir, err := s.files.Extract(ctx, msg.Path)
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	return deviceproto.WriteMessage(conn, deviceproto.FrameExtracted, deviceproto.ExtractedMsg{Dir: dir})
}

// frameWriter buffers written bytes into data frames.
type frameWriter struct {
	conn   net.Conn
	buffer []byte
	err    error
}

func (w *frameWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	written := len(p)
	for len(p) > 0 {
		room := deviceproto.DataChunkSize - len(w.buffer)
		take := min(room, len(p))
		w.buffer = append(w.buffer, p[:take]...)
		p = p[take:]
		if len(w.buffer) == deviceproto.DataChunkSize {
			if err := w.flush(); err != nil {
				return 0, err
			}
		}
	}
	return written, nil
}

func (w *frameWriter) flush() error {
	if w.err != nil || len(w.buffer) == 0 {
		return w.err
	}
	w.err = deviceproto.WriteFrame(w.conn, deviceproto.FrameData, w.buffer)
	w.buffer = w.buffer[:0]
	return w.err
}
