package remotedesktop

import (
	"errors"
	"io"
	"net"
	"sync"
)

// frameStream exposes a FrameConn as a byte stream (noVNC treats websocket messages as chunks of a stream).
type frameStream struct {
	fc  FrameConn
	buf []byte
}

func (s *frameStream) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		frame, err := s.fc.ReadFrame()
		if err != nil {
			return 0, err
		}
		s.buf = frame
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func (s *frameStream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := s.fc.WriteFrame(append([]byte(nil), p...)); err != nil {
		return 0, err
	}
	return len(p), nil
}

const relayChunk = 32 * 1024

// relayFrames copies bytes between the frame transport and the upstream connection until either side ends. Writes
// to the client block (backpressure) instead of dropping data. It closes both sides on return.
func relayFrames(client FrameConn, upstream net.Conn, clientReader io.Reader) error {
	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			_ = upstream.Close()
			_ = client.Close()
		})
	}
	defer closeAll()
	errs := make(chan error, 2)
	go func() {
		buf := make([]byte, relayChunk)
		for {
			n, err := upstream.Read(buf)
			if n > 0 {
				if werr := client.WriteFrame(append([]byte(nil), buf[:n]...)); werr != nil {
					errs <- werr
					return
				}
			}
			if err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		if clientReader != nil {
			if _, err := io.Copy(upstream, clientReader); err != nil {
				errs <- err
			} else {
				errs <- io.EOF
			}
			return
		}
		for {
			frame, err := client.ReadFrame()
			if err != nil {
				errs <- err
				return
			}
			if _, err := upstream.Write(frame); err != nil {
				errs <- err
				return
			}
		}
	}()
	err := <-errs
	closeAll()
	<-errs
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// vncFacadeHandshake speaks RFB 3.8 to noVNC and offers only security type None: the credentials stay in Go.
func vncFacadeHandshake(client io.ReadWriter) error {
	if _, err := client.Write([]byte("RFB 003.008\n")); err != nil {
		return err
	}
	version := make([]byte, 12)
	if _, err := io.ReadFull(client, version); err != nil {
		return err
	}
	if string(version[:4]) != "RFB " {
		return userErr("客户端不是 VNC (RFB) 客户端")
	}
	if _, err := client.Write([]byte{1, rfbSecNone}); err != nil {
		return err
	}
	choice := make([]byte, 1)
	if _, err := io.ReadFull(client, choice); err != nil {
		return err
	}
	if choice[0] != rfbSecNone {
		return userErr("客户端选择了不受支持的 VNC 安全类型")
	}
	_, err := client.Write([]byte{0, 0, 0, 0})
	return err
}

// vncRejectClient delivers a failure reason to noVNC in the security negotiation ("zero security types").
func vncRejectClient(client io.ReadWriter, reason string) {
	if _, err := client.Write([]byte("RFB 003.008\n")); err != nil {
		return
	}
	if _, err := io.ReadFull(client, make([]byte, 12)); err != nil {
		return
	}
	msg := []byte{0, 0, 0, 0, 0}
	msg[1] = byte(len(reason) >> 24)
	msg[2] = byte(len(reason) >> 16)
	msg[3] = byte(len(reason) >> 8)
	msg[4] = byte(len(reason))
	_, _ = client.Write(append(msg, reason...))
}
