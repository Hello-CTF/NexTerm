package app

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"time"
)

const (
	defaultRequestReadIdleTimeout  = time.Minute
	defaultRequestWriteIdleTimeout = time.Minute
)

// withRequestTimeouts applies read/write idle timeouts to normal HTTP requests: every body read and every response
// write renews the deadline, so slow but steadily transferring connections are unaffected; idle keep-alive is governed
// by http.Server's IdleTimeout, and request headers by ReadHeaderTimeout.
// Once a connection is hijacked (WebSocket and other long-lived connections), all deadlines are cleared and the upper layer takes over.
func withRequestTimeouts(readIdle, writeIdle time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = &idleTimeoutReadCloser{ReadCloser: r.Body, controller: controller, idle: readIdle}
		}
		next.ServeHTTP(&idleTimeoutResponseWriter{ResponseWriter: w, controller: controller, idle: writeIdle}, r)
		// Buffered output is flushed after the handler returns; renew the deadline so long handler work
		// since the last Write does not make that final flush fail.
		_ = controller.SetWriteDeadline(time.Now().Add(writeIdle))
	})
}

type idleTimeoutReadCloser struct {
	io.ReadCloser
	controller *http.ResponseController
	idle       time.Duration
}

func (r *idleTimeoutReadCloser) Read(data []byte) (int, error) {
	_ = r.controller.SetReadDeadline(time.Now().Add(r.idle))
	return r.ReadCloser.Read(data)
}

type idleTimeoutResponseWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	idle       time.Duration
}

func (w *idleTimeoutResponseWriter) Write(data []byte) (int, error) {
	_ = w.controller.SetWriteDeadline(time.Now().Add(w.idle))
	return w.ResponseWriter.Write(data)
}

func (w *idleTimeoutResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	connection, readWriter, err := w.controller.Hijack()
	if err != nil {
		return nil, nil, err
	}
	_ = connection.SetDeadline(time.Time{})
	return connection, readWriter, nil
}

func (w *idleTimeoutResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *idleTimeoutResponseWriter) Flush() {
	_ = w.controller.SetWriteDeadline(time.Now().Add(w.idle))
	_ = w.controller.Flush()
}
