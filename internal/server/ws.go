package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
)

const (
	DefaultWebSocketKeepAlive    = 25 * time.Second
	DefaultWebSocketPingTimeout  = 10 * time.Second
	DefaultWebSocketWriteTimeout = 10 * time.Second
)

type WebSocketConfig struct {
	KeepAlive    time.Duration
	PingTimeout  time.Duration
	WriteTimeout time.Duration
}

func (c WebSocketConfig) withDefaults() WebSocketConfig {
	if c.KeepAlive == 0 {
		c.KeepAlive = DefaultWebSocketKeepAlive
	}
	if c.PingTimeout <= 0 {
		c.PingTimeout = DefaultWebSocketPingTimeout
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = DefaultWebSocketWriteTimeout
	}
	return c
}

func ParseWebSocketEnv(getenv func(string) string) (WebSocketConfig, error) {
	var config WebSocketConfig
	for _, entry := range []struct {
		key    string
		target *time.Duration
	}{
		{"NEXTERM_WS_KEEPALIVE", &config.KeepAlive},
		{"NEXTERM_WS_PING_TIMEOUT", &config.PingTimeout},
		{"NEXTERM_WS_WRITE_TIMEOUT", &config.WriteTimeout},
	} {
		raw := getenv(entry.key)
		if raw == "" {
			continue
		}
		value, err := time.ParseDuration(raw)
		if err != nil {
			return WebSocketConfig{}, fmt.Errorf("%s must be a duration: %w", entry.key, err)
		}
		*entry.target = value
	}
	return config, nil
}

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
	since, present, resync := parseEventCursor(r)
	subscriber, unsubscribe, err := s.events.subscribe(since, present, resync)
	if err != nil {
		_ = connection.CloseNow()
		return
	}
	defer unsubscribe()
	s.pumpSocket(ctx, connection, func(ctx context.Context) (websocket.MessageType, []byte, func(), error) {
		select {
		case data := <-subscriber.queue:
			return websocket.MessageText, data, nil, nil
		case <-subscriber.done:
			return 0, nil, nil, ErrEventBrokerClosed
		case <-ctx.Done():
			return 0, nil, nil, ctx.Err()
		}
	})
}

func parseEventCursor(r *http.Request) (since uint64, present, resync bool) {
	raw := r.URL.Query().Get("since")
	if raw == "" {
		return 0, false, false
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, true, true
	}
	return value, true, false
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
	s.pumpSocket(ctx, connection, func(ctx context.Context) (websocket.MessageType, []byte, func(), error) {
		frame, err := receiver.Next(ctx)
		if err != nil {
			return 0, nil, nil, err
		}
		ack := func() { _ = receiver.Ack(frame.Sequence) }
		switch frame.Kind {
		case FrameBinary:
			return websocket.MessageBinary, frame.Data, ack, nil
		case FrameJSON:
			return websocket.MessageText, frame.Data, ack, nil
		default:
			return 0, nil, nil, errors.New("unknown channel frame kind")
		}
	})
}

func (s *Server) pumpSocket(ctx context.Context, connection *websocket.Conn, next func(context.Context) (websocket.MessageType, []byte, func(), error)) {
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
	if s.webSocket.KeepAlive > 0 {
		go s.keepAliveSocket(ctx, connection)
	}
	defer func() {
		cancel()
		_ = connection.CloseNow()
		<-readDone
	}()

	for {
		messageType, data, ack, err := next(ctx)
		if err != nil {
			return
		}
		writeCtx, writeCancel := context.WithTimeout(ctx, s.webSocket.WriteTimeout)
		err = connection.Write(writeCtx, messageType, data)
		writeCancel()
		if err != nil {
			return
		}
		if ack != nil {
			ack()
		}
	}
}

func (s *Server) keepAliveSocket(ctx context.Context, connection *websocket.Conn) {
	ticker := time.NewTicker(s.webSocket.KeepAlive)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		pingCtx, cancel := context.WithTimeout(ctx, s.webSocket.PingTimeout)
		err := connection.Ping(pingCtx)
		cancel()
		if err != nil {
			_ = connection.CloseNow()
			return
		}
	}
}
