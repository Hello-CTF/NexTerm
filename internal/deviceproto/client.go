package deviceproto

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// Client runs one operation on its connection. Unary operations leave conn
// ownership with the caller; the stream constructors (OpenRead, OpenWrite,
// Dial) take ownership of conn in every outcome, closing it on failure and
// handing it to the returned stream on success.
type Client struct {
	conn net.Conn
}

func NewClient(conn net.Conn) *Client {
	return &Client{conn: conn}
}

// exchange tracks one in-flight operation: the ctx-cancel escape closes the
// conn so a blocked read always unblocks, and stop releases that hook.
type exchange struct {
	ctx  context.Context
	conn net.Conn
	stop func() bool
}

func (c *Client) begin(ctx context.Context, kind FrameType, message any) (*exchange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ex := &exchange{ctx: ctx, conn: c.conn}
	ex.stop = context.AfterFunc(ctx, func() { _ = c.conn.Close() })
	if err := WriteMessage(c.conn, kind, message); err != nil {
		ex.stop()
		return nil, ex.err(err)
	}
	return ex, nil
}

func (ex *exchange) finish() {
	ex.stop()
}

// err prefers the context error: after a cancel the conn close surfaces as a
// network error, which would mask the real cause.
func (ex *exchange) err(err error) error {
	if ctxErr := ex.ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

// next reads one reply frame, converting FrameError payloads to errors.
func (ex *exchange) next() (FrameType, []byte, error) {
	kind, payload, err := ReadFrame(ex.conn)
	if err != nil {
		return 0, nil, ex.err(err)
	}
	if kind == FrameError {
		return 0, nil, ReadError(payload)
	}
	return kind, payload, nil
}

func (ex *exchange) unexpected(kind FrameType) error {
	return fmt.Errorf("%w: unexpected reply frame %d", ErrProtocol, byte(kind))
}

// expectOK reads the single FrameOK concluding a unary operation.
func (ex *exchange) expectOK() error {
	kind, _, err := ex.next()
	if err != nil {
		return err
	}
	if kind != FrameOK {
		return ex.unexpected(kind)
	}
	return nil
}

// readFileData collects a FrameFileData header plus its chunk stream,
// refusing growth past limit the way the SSH filesystem does.
func (ex *exchange) readFileData(limit int64) (int64, []byte, error) {
	kind, payload, err := ex.next()
	if err != nil {
		return 0, nil, err
	}
	if kind != FrameFileData {
		return 0, nil, ex.unexpected(kind)
	}
	var header FileDataMsg
	if err := UnmarshalPayload(payload, &header); err != nil {
		return 0, nil, err
	}
	var data []byte
	for {
		kind, payload, err := ex.next()
		if err != nil {
			return 0, nil, err
		}
		switch kind {
		case FrameData:
			data = append(data, payload...)
			if int64(len(data)) > limit {
				return 0, nil, fmt.Errorf("remote file grew beyond read limit of %d bytes", limit)
			}
		case FrameEnd:
			return header.Size, data, nil
		default:
			return 0, nil, ex.unexpected(kind)
		}
	}
}

func (c *Client) unary(ctx context.Context, kind FrameType, message any) error {
	ex, err := c.begin(ctx, kind, message)
	if err != nil {
		return err
	}
	defer ex.finish()
	return ex.expectOK()
}

func (c *Client) List(ctx context.Context, path string) ([]base.FileEntry, error) {
	ex, err := c.begin(ctx, FrameList, PathMsg{Path: path})
	if err != nil {
		return nil, err
	}
	defer ex.finish()
	var entries []base.FileEntry
	for {
		kind, payload, err := ex.next()
		if err != nil {
			return nil, err
		}
		switch kind {
		case FrameListed:
			var chunk ListChunk
			if err := UnmarshalPayload(payload, &chunk); err != nil {
				return nil, err
			}
			entries = append(entries, chunk.Entries...)
		case FrameEnd:
			return entries, nil
		default:
			return nil, ex.unexpected(kind)
		}
	}
}

func (c *Client) ReadFile(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	ex, err := c.begin(ctx, FrameReadFile, ReadMsg{Path: path, MaxBytes: maxBytes})
	if err != nil {
		return nil, err
	}
	defer ex.finish()
	_, data, err := ex.readFileData(maxBytes)
	return data, err
}

// ReadRange returns up to maxBytes from offset plus the total file size.
func (c *Client) ReadRange(ctx context.Context, path string, offset, maxBytes int64) ([]byte, int64, error) {
	ex, err := c.begin(ctx, FrameReadRange, RangeMsg{Path: path, Offset: offset, MaxBytes: maxBytes})
	if err != nil {
		return nil, 0, err
	}
	defer ex.finish()
	size, data, err := ex.readFileData(maxBytes)
	if err != nil {
		return nil, 0, err
	}
	return data, size, nil
}

// WriteFile streams the payload as data frames after the header; the reply
// reader runs from the start so an early server error cannot deadlock a
// fully synchronous transport.
func (c *Client) WriteFile(ctx context.Context, path string, data []byte, backup bool) error {
	if int64(len(data)) > MaxWriteFileSize {
		return fmt.Errorf("%w: write of %d bytes exceeds limit %d", ErrInvalid, len(data), MaxWriteFileSize)
	}
	stream, err := c.beginStream(ctx, FrameWriteFile, WriteMsg{Path: path, Backup: backup, Size: int64(len(data))})
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
	if err := stream.send(FrameEnd, nil); err != nil {
		return err
	}
	return stream.wait()
}

func (c *Client) Mkdir(ctx context.Context, path string) error {
	return c.unary(ctx, FrameMkdir, PathMsg{Path: path})
}

func (c *Client) Rename(ctx context.Context, from, to string) error {
	return c.unary(ctx, FrameRename, RenameMsg{From: from, To: to})
}

func (c *Client) Delete(ctx context.Context, path string, dir bool) error {
	return c.unary(ctx, FrameDelete, DeleteMsg{Path: path, Dir: dir})
}

func (c *Client) Chmod(ctx context.Context, path string, mode fs.FileMode) error {
	return c.unary(ctx, FrameChmod, ChmodMsg{Path: path, Mode: mode})
}

func (c *Client) Checksum(ctx context.Context, path, algorithm string) (string, error) {
	ex, err := c.begin(ctx, FrameChecksum, ChecksumMsg{Path: path, Algorithm: algorithm})
	if err != nil {
		return "", err
	}
	defer ex.finish()
	kind, payload, err := ex.next()
	if err != nil {
		return "", err
	}
	if kind != FrameChecksumReply {
		return "", ex.unexpected(kind)
	}
	var reply ChecksumReply
	if err := UnmarshalPayload(payload, &reply); err != nil {
		return "", err
	}
	return reply.Sum, nil
}

// Stat returns the file size; a missing path comes back as fs.ErrNotExist.
func (c *Client) Stat(ctx context.Context, path string) (int64, error) {
	ex, err := c.begin(ctx, FrameStat, PathMsg{Path: path})
	if err != nil {
		return 0, err
	}
	defer ex.finish()
	kind, payload, err := ex.next()
	if err != nil {
		return 0, err
	}
	if kind != FrameStatReply {
		return 0, ex.unexpected(kind)
	}
	var reply StatReply
	if err := UnmarshalPayload(payload, &reply); err != nil {
		return 0, err
	}
	return reply.Size, nil
}

// OpenRead opens the read stream; on any outcome the client owns the conn.
func (c *Client) OpenRead(ctx context.Context, path string) (*ReadStream, error) {
	ex, err := c.begin(ctx, FrameOpenRead, PathMsg{Path: path})
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

// ReadStream is the client half of an open-read operation.
type ReadStream struct {
	*exchange
	size      int64
	buf       []byte
	closeOnce sync.Once
	closeErr  error
}

func (s *ReadStream) Size() int64 {
	return s.size
}

func (s *ReadStream) Read(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	for len(s.buf) == 0 {
		kind, payload, err := s.next()
		if err != nil {
			return 0, err
		}
		switch kind {
		case FrameData:
			s.buf = payload
		case FrameEnd:
			return 0, io.EOF
		default:
			return 0, s.unexpected(kind)
		}
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func (s *ReadStream) Close() error {
	s.closeOnce.Do(func() {
		s.finish()
		s.closeErr = s.conn.Close()
	})
	return s.closeErr
}

var _ base.RemoteReader = (*ReadStream)(nil)

// streamExchange adds a reply reader for operations whose request phase
// streams frames (WriteFile, OpenWrite): the server answers data errors
// asynchronously, and on fully synchronous transports a reply is only
// deliverable while a reader is parked on the conn. The reader starts via
// start() once the caller finished any synchronous handshake.
type streamExchange struct {
	*exchange
	mu         sync.Mutex
	terminal   error
	resp       chan error
	readerDone chan struct{}
}

func (c *Client) beginStream(ctx context.Context, kind FrameType, message any) (*streamExchange, error) {
	ex, err := c.begin(ctx, kind, message)
	if err != nil {
		return nil, err
	}
	return &streamExchange{
		exchange:   ex,
		resp:       make(chan error, 1),
		readerDone: make(chan struct{}),
	}, nil
}

func (s *streamExchange) start() {
	go s.readReplies()
}

func (s *streamExchange) readReplies() {
	defer close(s.readerDone)
	for {
		kind, payload, err := ReadFrame(s.conn)
		switch {
		case err != nil:
			s.fail(s.err(err))
			return
		case kind == FrameOK:
			s.deliver(nil)
		case kind == FrameError:
			s.fail(ReadError(payload))
			return
		default:
			s.fail(fmt.Errorf("%w: unexpected stream reply frame %d", ErrProtocol, byte(kind)))
			return
		}
	}
}

func (s *streamExchange) deliver(err error) {
	select {
	case s.resp <- err:
	default:
	}
}

func (s *streamExchange) fail(err error) {
	s.mu.Lock()
	if s.terminal == nil {
		s.terminal = err
	}
	s.mu.Unlock()
	s.deliver(err)
}

func (s *streamExchange) send(kind FrameType, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal != nil {
		return s.terminal
	}
	if err := WriteFrame(s.conn, kind, payload); err != nil {
		terminal := s.err(err)
		// A server that fails the operation reports a coded FrameError before
		// closing; prefer it over the local write failure when it is here.
		select {
		case remote := <-s.resp:
			if remote != nil {
				terminal = remote
			}
		default:
		}
		s.terminal = terminal
		return s.terminal
	}
	return nil
}

// wait blocks for the reply to one sync/close/end request; the reply reader
// always delivers, including on conn failure.
func (s *streamExchange) wait() error {
	return <-s.resp
}

// shutdown releases the cancel hook, closes the conn, and joins the reader.
func (s *streamExchange) shutdown() error {
	s.finish()
	_ = s.conn.Close()
	<-s.readerDone
	return nil
}

// OpenWrite opens the write stream; on any outcome the client owns the conn.
func (c *Client) OpenWrite(ctx context.Context, path string, appendMode bool) (*WriteStream, error) {
	stream, err := c.beginStream(ctx, FrameOpenWrite, OpenWriteMsg{Path: path, Append: appendMode})
	if err != nil {
		_ = c.conn.Close()
		return nil, err
	}
	fail := func(err error) (*WriteStream, error) {
		stream.finish()
		_ = c.conn.Close()
		return nil, err
	}
	kind, _, err := stream.next()
	if err != nil {
		return fail(err)
	}
	if kind != FrameOpened {
		return fail(stream.unexpected(kind))
	}
	stream.start()
	return &WriteStream{stream: stream}, nil
}

// WriteStream is the client half of an open-write operation.
type WriteStream struct {
	stream    *streamExchange
	closeOnce sync.Once
	closeErr  error
}

func (w *WriteStream) Write(p []byte) (int, error) {
	if err := w.stream.ctx.Err(); err != nil {
		return 0, err
	}
	var written int
	for len(p) > 0 {
		chunk := p
		if len(chunk) > DataChunkSize {
			chunk = chunk[:DataChunkSize]
		}
		if err := w.stream.send(FrameData, chunk); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

func (w *WriteStream) Sync() error {
	if err := w.stream.ctx.Err(); err != nil {
		return err
	}
	if err := w.stream.send(FrameSync, nil); err != nil {
		return err
	}
	return w.stream.wait()
}

func (w *WriteStream) Close() error {
	w.closeOnce.Do(func() {
		w.closeErr = w.stream.send(FrameClose, nil)
		if w.closeErr == nil {
			w.closeErr = w.stream.wait()
		}
		_ = w.stream.shutdown()
	})
	return w.closeErr
}

var _ base.RemoteWriter = (*WriteStream)(nil)

// Dial requests a proxied connection; on any outcome the client owns the conn.
func (c *Client) Dial(ctx context.Context, network, address string) (*DialStream, error) {
	ex, err := c.begin(ctx, FrameDial, DialMsg{Network: network, Address: address})
	if err != nil {
		_ = c.conn.Close()
		return nil, err
	}
	fail := func(err error) (*DialStream, error) {
		ex.finish()
		_ = c.conn.Close()
		return nil, err
	}
	kind, _, err := ex.next()
	if err != nil {
		return fail(err)
	}
	if kind != FrameDialed {
		return fail(ex.unexpected(kind))
	}
	// The dial ctx governs the handshake only: callers dial with a timeout
	// ctx and cancel right after DialContext returns (the forward service
	// does), and the proxied connection must survive that, mirroring SSH
	// channel semantics. Disarm the cancel hook and decouple the stream.
	ex.stop()
	ex.ctx = context.Background()
	return &DialStream{exchange: ex}, nil
}

// DialStream is the client half of a proxied dial: data frames carry the
// byte stream and a FrameDialEOF marks each direction's half-close.
type DialStream struct {
	*exchange
	writeMu   sync.Mutex
	eofSent   bool
	readEOF   bool
	buf       []byte
	closeOnce sync.Once
	closeErr  error
}

func (s *DialStream) Read(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if s.readEOF {
		return 0, io.EOF
	}
	for len(s.buf) == 0 {
		kind, payload, err := s.next()
		if err != nil {
			return 0, err
		}
		switch kind {
		case FrameDialData:
			s.buf = payload
		case FrameDialEOF:
			s.readEOF = true
			return 0, io.EOF
		default:
			return 0, s.unexpected(kind)
		}
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func (s *DialStream) Write(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.eofSent {
		return 0, io.ErrClosedPipe
	}
	var written int
	for len(p) > 0 {
		chunk := p
		if len(chunk) > DataChunkSize {
			chunk = chunk[:DataChunkSize]
		}
		if err := WriteFrame(s.conn, FrameDialData, chunk); err != nil {
			return written, s.err(err)
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

// CloseWrite signals the half-close: the target sees EOF after the buffered
// bytes, and later Writes fail.
func (s *DialStream) CloseWrite() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.eofSent {
		return nil
	}
	s.eofSent = true
	if err := WriteFrame(s.conn, FrameDialEOF, nil); err != nil {
		return s.err(err)
	}
	return nil
}

func (s *DialStream) Close() error {
	s.closeOnce.Do(func() {
		s.finish()
		s.closeErr = s.conn.Close()
	})
	return s.closeErr
}

var _ base.WriteCloseStream = (*DialStream)(nil)
