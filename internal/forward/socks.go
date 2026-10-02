package forward

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
	"unicode/utf8"
)

const (
	socksVersion            = 5
	socksNoAuth             = 0
	socksNoAcceptable       = 0xff
	socksConnect            = 1
	socksSucceeded          = 0
	socksGeneralFailure     = 1
	socksConnectionRefused  = 5
	socksCommandUnsupported = 7
	socksAddressUnsupported = 8
)

var errSOCKSVersion = errors.New("unsupported SOCKS version")

func (s *Service) serveSOCKS(f *forwarder, client net.Conn) error {
	if err := client.SetDeadline(time.Now().Add(s.handshakeTimeout)); err != nil {
		return err
	}
	if err := negotiateSOCKS(client); err != nil {
		return err
	}
	target, command, err := readSOCKSRequest(client)
	if err != nil {
		_ = writeSOCKSReply(client, socksAddressUnsupported)
		return err
	}
	if command != socksConnect {
		_ = writeSOCKSReply(client, socksCommandUnsupported)
		return fmt.Errorf("SOCKS 命令 %d 不受支持", command)
	}
	upstream, err := s.openUpstream(f.ctx, f.sessionID, target)
	if err != nil {
		_ = writeSOCKSReply(client, socksConnectionRefused)
		return err
	}
	if err := writeSOCKSReply(client, socksSucceeded); err != nil {
		_ = upstream.Close()
		return err
	}
	if err := client.SetDeadline(time.Time{}); err != nil {
		_ = upstream.Close()
		return err
	}
	return f.relayTracked(client, upstream)
}

func negotiateSOCKS(conn net.Conn) error {
	var header [2]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return err
	}
	if header[0] != socksVersion {
		return errSOCKSVersion
	}
	if header[1] == 0 {
		_ = writeAll(conn, []byte{socksVersion, socksNoAcceptable})
		return errors.New("SOCKS 客户端未提供认证方法")
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	for _, method := range methods {
		if method == socksNoAuth {
			return writeAll(conn, []byte{socksVersion, socksNoAuth})
		}
	}
	if err := writeAll(conn, []byte{socksVersion, socksNoAcceptable}); err != nil {
		return err
	}
	return errors.New("SOCKS 客户端不支持无认证模式")
}

func readSOCKSRequest(conn net.Conn) (string, byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return "", 0, err
	}
	if header[0] != socksVersion {
		return "", 0, errSOCKSVersion
	}
	if header[2] != 0 {
		return "", 0, errors.New("SOCKS RSV 必须为 0")
	}
	var host string
	switch header[3] {
	case 1:
		var address [4]byte
		if _, err := io.ReadFull(conn, address[:]); err != nil {
			return "", 0, err
		}
		host = net.IP(address[:]).String()
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return "", 0, err
		}
		if length[0] == 0 {
			return "", 0, errors.New("SOCKS 域名为空")
		}
		address := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, address); err != nil {
			return "", 0, err
		}
		if !utf8.Valid(address) {
			return "", 0, errors.New("SOCKS 域名不是有效 UTF-8")
		}
		host = string(address)
	case 4:
		var address [16]byte
		if _, err := io.ReadFull(conn, address[:]); err != nil {
			return "", 0, err
		}
		host = net.IP(address[:]).String()
	default:
		return "", 0, fmt.Errorf("SOCKS 地址类型 %d 不受支持", header[3])
	}
	var port [2]byte
	if _, err := io.ReadFull(conn, port[:]); err != nil {
		return "", 0, err
	}
	value := binary.BigEndian.Uint16(port[:])
	if value == 0 {
		return "", 0, errors.New("SOCKS 目标端口为 0")
	}
	return net.JoinHostPort(host, strconv.Itoa(int(value))), header[1], nil
}

func writeSOCKSReply(conn net.Conn, reply byte) error {
	return writeAll(conn, []byte{socksVersion, reply, 0, 1, 0, 0, 0, 0, 0, 0})
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
