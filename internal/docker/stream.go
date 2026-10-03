package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

type streamKind int

const (
	streamLogs streamKind = iota
	streamExec
)

type streamRegistry struct {
	mu      sync.Mutex
	streams map[string]*managedStream
	epochs  map[string]uint64
	closed  bool
	counter atomic.Uint64
}

func newStreamRegistry() *streamRegistry {
	return &streamRegistry{streams: make(map[string]*managedStream), epochs: make(map[string]uint64)}
}

func (r *streamRegistry) nextID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("docker-%d", r.counter.Add(1))
}

func (r *streamRegistry) token(sessionID string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.epochs[sessionID]
}

func (r *streamRegistry) add(stream *managedStream, token uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.epochs[stream.sessionID] != token {
		return ErrStreamClosed
	}
	r.streams[stream.id] = stream
	return nil
}

func (r *streamRegistry) get(id string) (*managedStream, error) {
	r.mu.Lock()
	stream := r.streams[id]
	r.mu.Unlock()
	if stream == nil {
		return nil, ErrStreamClosed
	}
	return stream, nil
}

func (r *streamRegistry) remove(id string, stream *managedStream) {
	r.mu.Lock()
	if r.streams[id] == stream {
		delete(r.streams, id)
	}
	r.mu.Unlock()
}

func (r *streamRegistry) closeSession(sessionID string) error {
	r.mu.Lock()
	r.epochs[sessionID]++
	ids := make([]string, 0)
	for id, stream := range r.streams {
		if stream.sessionID == sessionID {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	var result error
	for _, id := range ids {
		if stream, err := r.get(id); err == nil {
			result = errors.Join(result, stream.close(ErrStreamClosed))
		}
	}
	return result
}

func (r *streamRegistry) closeAll() error {
	r.mu.Lock()
	r.closed = true
	streams := make([]*managedStream, 0, len(r.streams))
	for _, stream := range r.streams {
		streams = append(streams, stream)
	}
	r.mu.Unlock()
	var result error
	for _, stream := range streams {
		result = errors.Join(result, stream.close(ErrStreamClosed))
	}
	return result
}

type managedStream struct {
	id        string
	sessionID string
	kind      streamKind
	registry  *streamRegistry
	reader    io.ReadCloser
	exec      ExecSession
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan StreamExit

	mu          sync.Mutex
	sink        FrameSink
	sinkCtx     context.Context
	sinkCancel  context.CancelFunc
	sinkVersion uint64
	closed      bool
	completed   bool
	exit        StreamExit
	writeMu     sync.Mutex
	deliveryMu  sync.Mutex
	replay      boundedReplay
	closeOnce   sync.Once
	finishOnce  sync.Once
	closeErr    error
}

func newManagedStream(registry *streamRegistry, sessionID string, kind streamKind, sink FrameSink, ctx context.Context, cancel context.CancelFunc) *managedStream {
	stream := &managedStream{
		id:        registry.nextID(),
		sessionID: sessionID,
		kind:      kind,
		registry:  registry,
		ctx:       ctx,
		cancel:    cancel,
		done:      make(chan StreamExit, 1),
	}
	stream.setSink(sink)
	return stream
}

func (s *managedStream) setSink(sink FrameSink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sinkCancel != nil {
		s.sinkCancel()
	}
	s.sinkVersion++
	s.sink = sink
	if sink != nil {
		s.sinkCtx, s.sinkCancel = context.WithCancel(s.ctx)
	} else {
		s.sinkCtx = nil
		s.sinkCancel = nil
	}
}

func (s *managedStream) currentSink() (FrameSink, context.Context, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sink, s.sinkCtx, s.sinkVersion
}

func (s *managedStream) sinkFailed(version uint64, frame []byte) {
	s.mu.Lock()
	if s.sinkVersion == version {
		if s.sinkCancel != nil {
			s.sinkCancel()
		}
		s.replay.add(frame)
		s.sink = nil
		s.sinkCtx = nil
		s.sinkCancel = nil
		s.sinkVersion++
	}
	s.mu.Unlock()
}

func (s *managedStream) dispatch(frame []byte) error {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	sink, sinkCtx, version := s.currentSink()
	if sink == nil {
		if s.kind == streamExec {
			s.mu.Lock()
			s.replay.add(frame)
			s.mu.Unlock()
		}
		return nil
	}
	if err := sink.Send(sinkCtx, frame); err != nil {
		if s.kind == streamExec {
			s.sinkFailed(version, frame)
			return nil
		}
		return err
	}
	return nil
}

func (s *managedStream) run() {
	buffer := make([]byte, 32*1024)
	var readErr error
	for {
		count, err := s.reader.Read(buffer)
		if count > 0 {
			frame := append([]byte(nil), buffer[:count]...)
			if sendErr := s.dispatch(frame); sendErr != nil {
				_ = s.close(sendErr)
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			break
		}
		if count == 0 {
			select {
			case <-s.ctx.Done():
				readErr = s.ctx.Err()
			default:
			}
			if readErr != nil {
				break
			}
		}
	}
	exitCode := 0
	if s.exec != nil && readErr == nil && s.ctx.Err() == nil {
		exitCode, readErr = s.exec.Wait(s.ctx)
	}
	s.finish(StreamExit{ExitCode: exitCode, Err: readErr})
}

func (s *managedStream) close(cause error) error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.mu.Lock()
		s.closed = true
		if s.sinkCancel != nil {
			s.sinkCancel()
		}
		s.mu.Unlock()
		if s.exec != nil {
			s.closeErr = s.exec.Close()
		} else if s.reader != nil {
			s.closeErr = s.reader.Close()
		}
		s.finish(StreamExit{ExitCode: -1, Err: cause})
		s.registry.remove(s.id, s)
	})
	return s.closeErr
}

func (s *managedStream) finish(exit StreamExit) {
	s.finishOnce.Do(func() {
		s.mu.Lock()
		s.completed = true
		s.exit = exit
		s.mu.Unlock()
		s.cancel()
		if s.exec != nil {
			_ = s.exec.Close()
		} else if s.reader != nil {
			_ = s.reader.Close()
		}
		s.done <- exit
		close(s.done)
		if s.kind == streamLogs {
			s.registry.remove(s.id, s)
		}
	})
}

func (s *managedStream) detach() error {
	if s.kind == streamLogs {
		return s.close(ErrStreamClosed)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrStreamClosed
	}
	if s.sinkCancel != nil {
		s.sinkCancel()
	}
	s.mu.Unlock()
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrStreamClosed
	}
	s.sink = nil
	s.sinkCtx = nil
	s.sinkCancel = nil
	s.sinkVersion++
	return nil
}

func (s *managedStream) resume(sink FrameSink) error {
	if s.kind != streamExec || sink == nil {
		return ErrUnsupported
	}
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrStreamClosed
	}
	if s.sink != nil {
		s.mu.Unlock()
		return errors.New("docker exec stream already has a subscriber")
	}
	if s.sinkCancel != nil {
		s.sinkCancel()
	}
	s.sinkVersion++
	version := s.sinkVersion
	s.sink = sink
	sinkParent := s.ctx
	if s.completed {
		sinkParent = context.Background()
	}
	s.sinkCtx, s.sinkCancel = context.WithCancel(sinkParent)
	sinkCtx := s.sinkCtx
	backlog := s.replay.drain()
	s.mu.Unlock()
	for index, frame := range backlog {
		if err := sink.Send(sinkCtx, frame); err != nil {
			s.mu.Lock()
			if s.sinkVersion == version {
				s.replay.restore(backlog[index:])
				if s.sinkCancel != nil {
					s.sinkCancel()
				}
				s.sink = nil
				s.sinkCtx = nil
				s.sinkCancel = nil
				s.sinkVersion++
			}
			s.mu.Unlock()
			return err
		}
	}
	return nil
}

func (s *Service) AttachLogs(ctx context.Context, request LogsAttachRequest) (string, error) {
	if request.Sink == nil {
		return "", errors.New("docker logs sink is required")
	}
	tail := 500
	if request.Tail != nil {
		tail = *request.Tail
		if tail < 0 {
			return "", errors.New("docker logs tail cannot be negative")
		}
	}
	token := s.streams.token(request.SessionID)
	setupCtx, cancel := context.WithTimeout(ctx, s.config.ListTimeout)
	defer cancel()
	streamCtx, streamCancel := context.WithCancel(context.Background())
	stopSetupCancel := context.AfterFunc(setupCtx, streamCancel)
	logStream, err := readWithFallback(setupCtx, s.provider, request.SessionID, func(backend Backend) (LogStream, error) {
		return backend.OpenLogs(streamCtx, LogsOptions{
			Container: request.Container,
			Tail:      tail,
			Follow:    true,
		})
	})
	if err != nil {
		streamCancel()
		return "", err
	}
	if !stopSetupCancel() && setupCtx.Err() != nil {
		_ = logStream.Reader.Close()
		return "", setupCtx.Err()
	}
	managed := newManagedStream(s.streams, request.SessionID, streamLogs, request.Sink, streamCtx, streamCancel)
	managed.reader = demultiplexReadCloser(logStream.Reader, logStream.TTY)
	if err := s.streams.add(managed, token); err != nil {
		_ = managed.close(err)
		return "", err
	}
	go managed.run()
	return managed.id, nil
}

func (s *Service) AttachExec(ctx context.Context, request ExecAttachRequest) (string, error) {
	if request.Sink == nil {
		return "", errors.New("docker exec sink is required")
	}
	request.Width = max(request.Width, 20)
	request.Height = max(request.Height, 5)
	token := s.streams.token(request.SessionID)
	setupCtx, cancel := context.WithTimeout(ctx, s.config.ListTimeout)
	defer cancel()
	streamCtx, streamCancel := context.WithCancel(context.Background())
	stopSetupCancel := context.AfterFunc(setupCtx, streamCancel)
	backend, err := s.mutationBackend(setupCtx, request.SessionID, request.CustomCmd != "")
	if err != nil {
		streamCancel()
		return "", err
	}
	var session ExecSession
	if request.CustomCmd != "" {
		opener, ok := backend.(interface {
			OpenRawExec(context.Context, string, uint, uint) (ExecSession, error)
		})
		if !ok {
			streamCancel()
			return "", fmt.Errorf("%w: custom command", ErrUnsupported)
		}
		session, err = opener.OpenRawExec(streamCtx, request.CustomCmd, request.Width, request.Height)
	} else {
		session, err = backend.OpenExec(streamCtx, ExecOptions{
			Container: request.Container,
			Cmd:       []string{"sh"},
			TTY:       true,
			Width:     request.Width,
			Height:    request.Height,
		})
	}
	if err != nil {
		streamCancel()
		return "", err
	}
	if !stopSetupCancel() && setupCtx.Err() != nil {
		_ = session.Close()
		return "", setupCtx.Err()
	}
	managed := newManagedStream(s.streams, request.SessionID, streamExec, request.Sink, streamCtx, streamCancel)
	managed.reader = session
	managed.exec = session
	managed.replay.limit = s.config.ExecReplayBytes
	if err := s.streams.add(managed, token); err != nil {
		_ = managed.close(err)
		return "", err
	}
	go managed.run()
	return managed.id, nil
}

func (s *Service) WriteExec(ctx context.Context, streamID string, data []byte) error {
	stream, err := s.streams.get(streamID)
	if err != nil {
		return err
	}
	if stream.kind != streamExec {
		return ErrUnsupported
	}
	stream.writeMu.Lock()
	defer stream.writeMu.Unlock()
	stream.mu.Lock()
	unavailable := stream.closed || stream.completed
	stream.mu.Unlock()
	if unavailable {
		return ErrStreamClosed
	}
	for len(data) > 0 {
		count, err := stream.exec.Write(data)
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
		data = data[count:]
	}
	return ctx.Err()
}

func (s *Service) ResizeExec(ctx context.Context, streamID string, width, height uint) error {
	stream, err := s.streams.get(streamID)
	if err != nil {
		return err
	}
	if stream.kind != streamExec {
		return ErrUnsupported
	}
	stream.mu.Lock()
	unavailable := stream.closed || stream.completed
	stream.mu.Unlock()
	if unavailable {
		return ErrStreamClosed
	}
	return stream.exec.Resize(ctx, max(width, 20), max(height, 5))
}

func (s *Service) DetachStream(streamID string) error {
	stream, err := s.streams.get(streamID)
	if err != nil {
		return nil
	}
	return stream.detach()
}

func (s *Service) ResumeExec(streamID string, sink FrameSink) error {
	stream, err := s.streams.get(streamID)
	if err != nil {
		return err
	}
	return stream.resume(sink)
}

func (s *Service) CloseStream(streamID string) error {
	stream, err := s.streams.get(streamID)
	if err != nil {
		return nil
	}
	return stream.close(ErrStreamClosed)
}

func (s *Service) StreamDone(streamID string) (<-chan StreamExit, error) {
	stream, err := s.streams.get(streamID)
	if err != nil {
		return nil, err
	}
	return stream.done, nil
}

func (s *Service) StreamStatus(streamID string) (StreamExit, bool, error) {
	stream, err := s.streams.get(streamID)
	if err != nil {
		return StreamExit{}, false, err
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.exit, stream.completed, nil
}

func (s *Service) StreamSession(streamID string) (string, error) {
	stream, err := s.streams.get(streamID)
	if err != nil {
		return "", err
	}
	return stream.sessionID, nil
}
