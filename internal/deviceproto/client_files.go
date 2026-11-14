package deviceproto

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/fs/conditional"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// WriteFileVersion replaces the file only while it still matches expected; a
// changed file fails with conditional.ErrVersionMismatch.
func (c *Client) WriteFileVersion(ctx context.Context, path string, data []byte, backup bool, expected conditional.Expectation) error {
	if int64(len(data)) > MaxWriteFileSize {
		return fmt.Errorf("%w: write of %d bytes exceeds limit %d", ErrInvalid, len(data), MaxWriteFileSize)
	}
	if err := expected.Validate(); err != nil {
		return err
	}
	stream, err := c.beginStream(ctx, FrameWriteVersion, WriteVersionMsg{Path: path, Backup: backup, Size: int64(len(data)), Expected: expected})
	if err != nil {
		return err
	}
	stream.start()
	defer func() { _ = stream.shutdown() }()
	for len(data) > 0 {
		chunk := data
		if len(chunk) > DataChunkSize {
			chunk = chunk[:DataChunkSize]
		}
		if err := stream.send(FrameData, chunk); err != nil {
			return err
		}
		data = data[len(chunk):]
	}
	stream.drainStale()
	if err := stream.send(FrameEnd, nil); err != nil {
		return err
	}
	return stream.wait()
}

// AtomicRename renames from over to, replacing an existing destination in one step.
func (c *Client) AtomicRename(ctx context.Context, from, to string) error {
	return c.unary(ctx, FrameAtomicRename, RenameMsg{From: from, To: to})
}

// StatInfo describes one path; a missing path comes back as fs.ErrNotExist.
func (c *Client) StatInfo(ctx context.Context, path string) (StatInfoReply, error) {
	ex, err := c.begin(ctx, FrameStatInfo, PathMsg{Path: path})
	if err != nil {
		return StatInfoReply{}, err
	}
	defer ex.finish()
	kind, payload, err := ex.next()
	if err != nil {
		return StatInfoReply{}, err
	}
	if kind != FrameStatInfoReply {
		return StatInfoReply{}, ex.unexpected(kind)
	}
	var reply StatInfoReply
	if err := UnmarshalPayload(payload, &reply); err != nil {
		return StatInfoReply{}, err
	}
	return reply, nil
}

// Watch starts a watch stream over the client's conn, which it owns in every
// outcome. Events reach sink from one goroutine; done receives the failure
// that ended the stream, or nil after Close.
func (c *Client) Watch(ctx context.Context, dirs []string, sink func(base.WatchEvent), done func(error)) (*WatchStream, error) {
	ex, err := c.begin(ctx, FrameWatch, WatchMsg{Dirs: nonNil(dirs)})
	if err != nil {
		_ = c.conn.Close()
		return nil, err
	}
	if err := ex.expectOK(); err != nil {
		ex.finish()
		_ = c.conn.Close()
		return nil, err
	}
	stream := &WatchStream{exchange: ex, finished: make(chan struct{})}
	go stream.read(sink, done)
	return stream, nil
}

// WatchStream is the client half of a watch operation.
type WatchStream struct {
	*exchange
	writeMu  sync.Mutex
	once     sync.Once
	closed   bool
	finished chan struct{}
}

func (s *WatchStream) read(sink func(base.WatchEvent), done func(error)) {
	var failure error
	defer func() {
		s.finish()
		_ = s.conn.Close()
		s.writeMu.Lock()
		closed := s.closed
		s.writeMu.Unlock()
		if closed {
			failure = nil
		}
		close(s.finished)
		if done != nil {
			done(failure)
		}
	}()
	for {
		kind, payload, err := s.next()
		if err != nil {
			failure = err
			return
		}
		if kind != FrameWatchEvent {
			failure = s.unexpected(kind)
			return
		}
		var event base.WatchEvent
		if err := UnmarshalPayload(payload, &event); err != nil {
			failure = err
			return
		}
		sink(event)
	}
}

// Set replaces the watched directories.
func (s *WatchStream) Set(dirs []string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed {
		return errors.New("file watch closed")
	}
	return WriteMessage(s.conn, FrameWatchSet, WatchMsg{Dirs: nonNil(dirs)})
}

// Close ends the stream and waits until its reader returned.
func (s *WatchStream) Close() error {
	s.once.Do(func() {
		s.writeMu.Lock()
		s.closed = true
		s.writeMu.Unlock()
		_ = s.conn.Close()
	})
	<-s.finished
	return nil
}

func nonNil(dirs []string) []string {
	if dirs == nil {
		return []string{}
	}
	return dirs
}

var _ base.WatchHandle = (*WatchStream)(nil)

// Pack opens a stream of the directory as a gzipped tar; the client owns the
// conn in every outcome. The stream's size is unknown (-1).
func (c *Client) Pack(ctx context.Context, path string) (*ReadStream, error) {
	ex, err := c.begin(ctx, FramePack, PathMsg{Path: path})
	if err != nil {
		_ = c.conn.Close()
		return nil, err
	}
	fail := func(err error) (*ReadStream, error) {
		ex.finish()
		_ = c.conn.Close()
		return nil, err
	}
	kind, payload, err := ex.next()
	if err != nil {
		return fail(err)
	}
	if kind != FrameOpened {
		return fail(ex.unexpected(kind))
	}
	var opened OpenedMsg
	if err := UnmarshalPayload(payload, &opened); err != nil {
		return fail(err)
	}
	return &ReadStream{exchange: ex, size: opened.Size}, nil
}

// Extract unpacks an archive next to it and returns the directory it went to.
func (c *Client) Extract(ctx context.Context, path string) (string, error) {
	ex, err := c.begin(ctx, FrameExtract, PathMsg{Path: path})
	if err != nil {
		return "", err
	}
	defer ex.finish()
	kind, payload, err := ex.next()
	if err != nil {
		return "", err
	}
	if kind != FrameExtracted {
		return "", ex.unexpected(kind)
	}
	var reply ExtractedMsg
	if err := UnmarshalPayload(payload, &reply); err != nil {
		return "", err
	}
	return reply.Dir, nil
}
