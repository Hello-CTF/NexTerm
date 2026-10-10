package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

var errReadTimeout = errors.New("read timed out")

type wsMessage struct {
	kind    websocket.MessageType
	payload []byte
	err     error
}

type wsConn struct {
	conn     *websocket.Conn
	messages chan wsMessage
	readErr  error
}

func dialWS(port int, path string, header http.Header) (*wsConn, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d%s", port, path), &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		return nil, status, fmt.Errorf("websocket handshake rejected: HTTP %d", status)
	}
	conn.SetReadLimit(1 << 20)
	w := &wsConn{conn: conn, messages: make(chan wsMessage, 64)}
	go func() {
		for {
			kind, payload, err := conn.Read(context.Background())
			if err != nil {
				w.messages <- wsMessage{err: err}
				return
			}
			w.messages <- wsMessage{kind: kind, payload: payload}
		}
	}()
	return w, 101, nil
}

func (w *wsConn) recv(timeout time.Duration) (wsMessage, error) {
	if w.readErr != nil {
		return wsMessage{err: w.readErr}, nil
	}
	select {
	case message := <-w.messages:
		if message.err != nil {
			w.readErr = message.err
		}
		return message, nil
	case <-time.After(timeout):
		return wsMessage{}, errReadTimeout
	}
}

func (w *wsConn) send(kind websocket.MessageType, payload []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return w.conn.Write(ctx, kind, payload)
}

func (w *wsConn) close() {
	w.conn.Close(websocket.StatusNormalClosure, "")
}

func wsHandshakeStatus(port int, path string) int {
	w, status, err := dialWS(port, path, nil)
	if err != nil {
		return status
	}
	w.close()
	return 101
}
