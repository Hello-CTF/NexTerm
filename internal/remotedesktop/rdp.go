package remotedesktop

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"syscall"
	"time"
)

// RDPSession is everything an RDP engine needs. The destination is always the stored asset's; whatever the client
// wrote into its request is ignored.
type RDPSession struct {
	Client FrameConn
	// First is the already read first client frame (the engine parses it as its request). Nil makes the engine read it.
	First []byte
	Dial  func(ctx context.Context) (net.Conn, error)
	Addr  string
	// VerifyCert applies trust on first use to the server chain, leaf first.
	VerifyCert func(chain [][]byte, leaf *x509.Certificate) error
	// Dialed is called once the upstream connection is established (for audit).
	Dialed func()
}

// RDPEngine is the swappable RDP back end. The shipped one is an RDCleanPath proxy for IronRDP (WASM).
type RDPEngine interface {
	Serve(ctx context.Context, session RDPSession) error
}

type rdCleanPathEngine struct{}

const (
	maxTPKT        = 64 * 1024
	x224Confirm    = 0xd0
	rdpNegResponse = 0x02
	rdpNegFailure  = 0x03
	protoTLS       = 0x1
	protoHybrid    = 0x2
	protoHybridEx  = 0x8

	wsaConnRefused  = 10061
	wsaTimedOut     = 10060
	wsaHostNotFound = 11001
	wsaGeneric      = 10050
)

func readTPKT(r io.Reader) ([]byte, error) {
	head, err := readFull(r, 4)
	if err != nil {
		return nil, err
	}
	if head[0] != 3 {
		return nil, userErr("RDP 服务器返回了无效的 TPKT 报文")
	}
	total := int(binary.BigEndian.Uint16(head[2:]))
	if total < 7 || total > maxTPKT {
		return nil, userErr("RDP 服务器返回了无效的 TPKT 长度")
	}
	rest, err := readFull(r, total-4)
	if err != nil {
		return nil, err
	}
	return append(head, rest...), nil
}

// selectedProtocol extracts the protocol of an X.224 Connection Confirm. failure reports RDP_NEG_FAILURE.
func selectedProtocol(cc []byte) (protocol uint32, failure bool, err error) {
	if len(cc) < 11 || cc[5] != x224Confirm {
		return 0, false, userErr("RDP 服务器没有返回 X.224 连接确认")
	}
	if len(cc) < 19 {
		return 0, false, nil // no negotiation block: standard RDP security
	}
	switch cc[11] {
	case rdpNegResponse:
		return binary.LittleEndian.Uint32(cc[15:19]), false, nil
	case rdpNegFailure:
		return 0, true, nil
	}
	return 0, false, nil
}

func wsaCode(err error) uint16 {
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr):
		return wsaHostNotFound
	case errors.Is(err, syscall.ECONNREFUSED):
		return wsaConnRefused
	case errors.Is(err, context.DeadlineExceeded):
		return wsaTimedOut
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return wsaTimedOut
	}
	return wsaGeneric
}

func (rdCleanPathEngine) Serve(ctx context.Context, session RDPSession) error {
	client := session.Client
	first := session.First
	if first == nil {
		frame, err := client.ReadFrame()
		if err != nil {
			return err
		}
		first = frame
	}
	request, err := parseRDCleanPath(first)
	if err != nil || request.Error != nil || !request.HasX224 || len(request.X224) == 0 {
		_ = client.WriteFrame(newRDCleanPathGeneralError())
		if err == nil {
			err = userErr("RDCleanPath 请求缺少 X.224 连接请求")
		}
		return userErrWrap("RDP 代理请求无效", err)
	}

	up, frame, tlsFailed, err := connectUpstream(ctx, session, request.X224, tls.VersionTLS12, true)
	if err != nil && tlsFailed && ctx.Err() == nil {
		// A server or middlebox that rejects or drops the TLS 1.2 hello may need TLS 1.3; the upstream connection is
		// single-use, so start over.
		up, frame, _, err = connectUpstream(ctx, session, request.X224, 0, false)
	}
	if err != nil {
		if frame != nil {
			_ = client.WriteFrame(frame)
		}
		return err
	}
	defer up.close()
	upstream, tlsConn, confirm, chain, leaf := up.conn, up.tls, up.confirm, up.chain, up.leaf
	if len(chain) == 0 {
		_ = client.WriteFrame(newRDCleanPathGeneralError())
		return userErr("RDP 服务器没有提供证书")
	}
	if session.VerifyCert != nil {
		if err := session.VerifyCert(chain, leaf); err != nil {
			_ = client.WriteFrame(newRDCleanPathHTTPError(403))
			return err
		}
	}
	_ = upstream.SetDeadline(time.Time{})
	if err := client.WriteFrame(newRDCleanPathResponse(session.Addr, confirm, chain)); err != nil {
		return err
	}
	return relayFrames(client, tlsConn, nil)
}

func hostOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func joinAddr(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

var _ = fmt.Sprintf

type rdpUpstream struct {
	conn    net.Conn
	tls     *tls.Conn
	confirm []byte
	chain   [][]byte
	leaf    *x509.Certificate
	close   func()
}

// connectUpstream dials the host, repeats the X.224 negotiation and completes the TLS handshake. maxVersion 0 allows
// every supported version. On failure it returns the RDCleanPath frame to send to the client, if any, and whether\n// the failure happened in the TLS handshake.
func connectUpstream(ctx context.Context, session RDPSession, x224 []byte, maxVersion uint16, first bool) (*rdpUpstream, []byte, bool, error) {
	upstream, err := session.Dial(ctx)
	if err != nil {
		return nil, newRDCleanPathWSAError(wsaCode(err)), false, err
	}
	stop := context.AfterFunc(ctx, func() { _ = upstream.Close() })
	closeAll := func() {
		stop()
		_ = upstream.Close()
	}
	if session.Dialed != nil && first {
		session.Dialed()
	}

	_ = upstream.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := upstream.Write(x224); err != nil {
		closeAll()
		return nil, newRDCleanPathWSAError(wsaCode(err)), false, userErrWrap("发送 RDP 连接请求失败", err)
	}
	confirm, err := readTPKT(upstream)
	if err != nil {
		closeAll()
		return nil, newRDCleanPathGeneralError(), false, err
	}
	protocol, failure, err := selectedProtocol(confirm)
	if err != nil {
		closeAll()
		return nil, newRDCleanPathGeneralError(), false, err
	}
	if failure || protocol&(protoTLS|protoHybrid|protoHybridEx) == 0 {
		closeAll()
		return nil, newRDCleanPathNegotiationError(confirm), false, userErr("RDP 服务器不支持 TLS/NLA 安全层（仅支持标准 RDP 安全），无法连接")
	}

	up := &rdpUpstream{conn: upstream, confirm: confirm, close: closeAll}
	up.tls = tls.Client(upstream, &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Windows Schannel rejects CredSSP over TLS 1.3, so offer 1.2 first; the caller retries without the cap.
		MaxVersion: maxVersion,
		// Pinned by fingerprint (trust on first use); RDP servers normally present self-signed certificates.
		InsecureSkipVerify: true,
		ServerName:         sniName(hostOf(session.Addr)),
		VerifyConnection: func(state tls.ConnectionState) error {
			for _, cert := range state.PeerCertificates {
				up.chain = append(up.chain, cert.Raw)
			}
			if len(state.PeerCertificates) > 0 {
				up.leaf = state.PeerCertificates[0]
			}
			return nil
		},
	})
	if err := up.tls.HandshakeContext(ctx); err != nil {
		closeAll()
		return nil, newRDCleanPathGeneralError(), true, userErrWrap("与 RDP 服务器的 TLS 握手失败", err)
	}
	return up, nil, false, nil
}
