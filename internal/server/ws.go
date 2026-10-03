package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/coder/websocket"
)

func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.options.AllowedOrigins})
	if err != nil {
		return
	}
	ctx, release, err := s.sockets.track(r.Context())
	if err != nil {
		_ = connection.CloseNow()
		return
	}
	defer release()
	subscriber, unsubscribe, err := s.events.subscribe()
	if err != nil {
		_ = connection.CloseNow()
		return
	}
	defer unsubscribe()
	s.pumpSocket(ctx, connection, func(ctx context.Context) (websocket.MessageType, []byte, error) {
		select {
		case data := <-subscriber.queue:
			return websocket.MessageText, data, nil
		case <-subscriber.done:
			return 0, nil, ErrEventBrokerClosed
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		}
	})
}

func (s *Server) serveChannel(w http.ResponseWriter, r *http.Request) {
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.options.AllowedOrigins})
	if err != nil {
		return
	}
	ctx, release, err := s.sockets.track(r.Context())
	if err != nil {
		_ = connection.CloseNow()
		return
	}
	defer release()
	receiver, err := s.channels.BindChannel(r.PathValue("id"))
	if err != nil {
		_ = connection.CloseNow()
		return
	}
	defer receiver.Close()
	s.pumpSocket(ctx, connection, func(ctx context.Context) (websocket.MessageType, []byte, error) {
		frame, err := receiver.Next(ctx)
		if err != nil {
			return 0, nil, err
		}
		switch frame.Kind {
		case FrameBinary:
			return websocket.MessageBinary, frame.Data, nil
		case FrameJSON:
			return websocket.MessageText, frame.Data, nil
		default:
			return 0, nil, errors.New("unknown channel frame kind")
		}
	})
}

func (s *Server) pumpSocket(ctx context.Context, connection *websocket.Conn, next func(context.Context) (websocket.MessageType, []byte, error)) {
	ctx, cancel := context.WithCancel(ctx)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer cancel()
		if s.readGate != nil {
			s.readGate()
		}
		for {
			if _, _, err := connection.Read(ctx); err != nil {
				return
			}
		}
	}()
	defer func() {
		cancel()
		// CloseNow tears the connection down without the close handshake:
		// Close would block (up to the library's 5s close-handshake timeout)
		// waiting for a peer close frame whenever the read loop has not
		// touched the connection yet, stalling the socket drain past the
		// Serve shutdown budget.
		_ = connection.CloseNow()
		<-readDone
	}()

	for {
		messageType, data, err := next(ctx)
		if err != nil {
			return
		}
		if err := connection.Write(ctx, messageType, data); err != nil {
			return
		}
	}
}
