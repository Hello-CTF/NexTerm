package production

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/atomicfile"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/logtail"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

const logTailSaveMaxBytes = 64 << 20

type logTailIPCService struct {
	tails   *logtail.Service
	desktop bool
}

type logTailCancelRequest struct {
	RunID string `json:"runId"`
}

type logTailSaveRequest struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"contentBase64"`
}

type logTailSaveResult struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

func newLogTailIPCService(sessions *session.Manager, desktop bool) *logTailIPCService {
	return &logTailIPCService{
		tails:   logtail.New(logtail.Config{Resolver: logTailResolver{sessions: sessions}}),
		desktop: desktop,
	}
}

func (s *logTailIPCService) register(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, "log_tail_start", func(ctx context.Context, call *ipc.Call, input logtail.Request) (logtail.StartResult, error) {
		ctx = productionSessionRequestContext(ctx)
		rawStream, err := call.Streams.OpenJSON(ctx, call.Channel)
		if err != nil {
			return logtail.StartResult{}, terminalIPCError(err)
		}
		stream := ipc.NewTypedJSONStream[logtail.Event](rawStream)
		result, err := s.tails.Start(ctx, ipc.OwnerID(ctx), input, &logTailJSONSink{stream: stream, raw: rawStream})
		if err != nil {
			_ = rawStream.Close()
			return logtail.StartResult{}, logTailIPCError(err)
		}
		return result, nil
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "log_tail_cancel", func(ctx context.Context, _ *ipc.Call, input logTailCancelRequest) (logtail.CancelResult, error) {
		result, err := s.tails.Cancel(ctx, ipc.OwnerID(ctx), input.RunID)
		if err != nil {
			return logtail.CancelResult{}, logTailIPCError(err)
		}
		return result, nil
	}); err != nil {
		return err
	}
	if !s.desktop {
		return nil
	}
	return ipc.Register(dispatcher, "log_tail_save", func(_ context.Context, _ *ipc.Call, input logTailSaveRequest) (logTailSaveResult, error) {
		if strings.TrimSpace(input.Path) == "" {
			return logTailSaveResult{}, ipc.BadParam(fmt.Errorf("保存路径不能为空"))
		}
		data, err := base64.StdEncoding.DecodeString(input.ContentBase64)
		if err != nil {
			return logTailSaveResult{}, ipc.BadParam(fmt.Errorf("日志内容不是有效的 Base64: %w", err))
		}
		if len(data) > logTailSaveMaxBytes {
			return logTailSaveResult{}, ipc.BadParam(fmt.Errorf("日志导出超过 %d 字节上限", logTailSaveMaxBytes))
		}
		if err := atomicfile.Write(input.Path, 0o600, func(writer io.Writer) error {
			_, err := writer.Write(data)
			return err
		}); err != nil {
			return logTailSaveResult{}, localFolderError(err)
		}
		return logTailSaveResult{Path: input.Path, Bytes: len(data)}, nil
	})
}

func (s *logTailIPCService) Shutdown(ctx context.Context) error {
	return s.tails.Shutdown(ctx)
}

type logTailJSONSink struct {
	stream *ipc.TypedJSONStream[logtail.Event]
	raw    ipc.JSONStream
}

func (s *logTailJSONSink) Send(ctx context.Context, event logtail.Event) error {
	return s.stream.Send(ctx, event)
}

func (s *logTailJSONSink) Close() error {
	return s.raw.Close()
}

func (s *logTailJSONSink) CloseGracefully() error {
	if graceful, ok := s.raw.(interface{ CloseGracefully() error }); ok {
		return graceful.CloseGracefully()
	}
	return s.Close()
}

type logTailResolver struct {
	sessions *session.Manager
}

func (r logTailResolver) Resolve(ctx context.Context, sessionID string) (logtail.Target, error) {
	if r.sessions == nil {
		return logtail.Target{}, fmt.Errorf("%w: session manager unavailable", session.ErrUnsupported)
	}
	connected, err := r.sessions.SessionFor(ctx, sessionID)
	if err != nil {
		return logtail.Target{}, err
	}
	info := connected.Info()
	if info.Kind != session.KindSSH {
		return logtail.Target{}, fmt.Errorf("%w: %s is %s", session.ErrUnsupported, sessionID, info.Kind)
	}
	if info.Status != session.StatusConnected {
		return logtail.Target{}, fmt.Errorf("%w: %s is %s", session.ErrDisconnected, sessionID, info.Status)
	}
	transport, err := r.sessions.TransportFor(ctx, sessionID)
	if err != nil {
		return logtail.Target{}, err
	}
	if !transport.IsAlive() {
		return logtail.Target{}, fmt.Errorf("%w: %s transport is not alive", session.ErrDisconnected, sessionID)
	}
	return logtail.Target{
		SessionID: info.ID, SessionName: info.Name,
		Transport: transport, Generation: transport.Generation(),
	}, nil
}

func logTailIPCError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, logtail.ErrInvalid):
		return ipc.BadParam(err)
	case errors.Is(err, logtail.ErrForbidden), errors.Is(err, session.ErrForbidden):
		return ipc.WrapError(ipc.CodeForbidden, err.Error(), err)
	case errors.Is(err, logtail.ErrConflict), errors.Is(err, logtail.ErrPendingLimit):
		return ipc.WrapError(ipc.Code("conflict"), err.Error(), err)
	case errors.Is(err, logtail.ErrUnsupported):
		return ipc.WrapError(ipc.CodeUnsupported, err.Error(), err)
	case errors.Is(err, logtail.ErrClosed):
		return ipc.WrapError(ipc.CodeDisconnected, err.Error(), err)
	default:
		return terminalIPCError(err)
	}
}
