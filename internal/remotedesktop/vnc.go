package remotedesktop

import (
	"context"
	"crypto/aes"
	"crypto/des"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"time"
)

// RFB security types and VeNCrypt subtypes.
const (
	rfbSecNone     = 1
	rfbSecVNCAuth  = 2
	rfbSecVeNCrypt = 19
	rfbSecARD      = 30

	vencryptPlain     = 256
	vencryptTLSNone   = 257
	vencryptTLSVnc    = 258
	vencryptTLSPlain  = 259
	vencryptX509None  = 260
	vencryptX509Vnc   = 261
	vencryptX509Plain = 262
)

// userError is shown to the user verbatim.
type userError struct {
	msg   string
	cause error
	// kind classifies the error for errors.Is without being shown to the user.
	kind error
}

func (e *userError) Is(target error) bool { return e.kind != nil && e.kind == target }

func (e *userError) Error() string {
	if e.cause != nil {
		return e.msg + ": " + e.cause.Error()
	}
	return e.msg
}
func (e *userError) Unwrap() error { return e.cause }

func userErr(msg string) error                  { return &userError{msg: msg} }
func userErrf(format string, a ...any) error    { return &userError{msg: fmt.Sprintf(format, a...)} }
func userErrWrap(msg string, cause error) error { return &userError{msg: msg, cause: cause} }

var errNoSecurityType = errors.New("no usable security type")

// errVeNCryptNoSubtype asks the caller to retry without VeNCrypt.
var errVeNCryptNoSubtype = errors.New("vencrypt: no supported subtype")

type vncUpstreamConfig struct {
	Username string
	Password string
	// VerifyCert is called with the TLS chain before any credential is sent (trust on first use).
	VerifyCert func(chain [][]byte, leaf *x509.Certificate) error
	// ServerName is only used for SNI; the certificate is pinned, not chain-verified.
	ServerName string
	// RequireTLS refuses every non-TLS security type: a certificate for this host was pinned before, so a server
	// that stops offering TLS must not receive credentials.
	RequireTLS bool
	// Timeout bounds the whole security handshake (0 means 30 seconds).
	Timeout time.Duration
}

var errTLSDowngrade = userErr("服务器不再提供 TLS 加密，与已记录的证书信任不符，已拒绝连接（未发送任何凭据）；如服务器配置确实已更改，请先在已知主机中删除该证书记录")

// vncDialUpstream connects, completes RFB security itself and returns a connection positioned right after the
// SecurityResult (the next bytes are ClientInit / ServerInit).
func vncDialUpstream(ctx context.Context, dial func(context.Context) (net.Conn, error), cfg vncUpstreamConfig) (net.Conn, error) {
	vencryptFailed := false
	for _, allowVeNCrypt := range []bool{true, false} {
		conn, err := dial(ctx)
		if err != nil {
			return nil, err
		}
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		_ = conn.SetDeadline(time.Now().Add(timeout))
		secured, err := vncUpstreamHandshake(conn, cfg, allowVeNCrypt)
		if err == nil {
			_ = secured.SetDeadline(time.Time{})
		}
		if !stop() && err == nil {
			_ = conn.Close()
			return nil, ctx.Err()
		}
		if err == nil {
			return secured, nil
		}
		_ = conn.Close()
		if errors.Is(err, errVeNCryptNoSubtype) && allowVeNCrypt && !cfg.RequireTLS {
			vencryptFailed = true
			continue
		}
		if errors.Is(err, errVeNCryptNoSubtype) && cfg.RequireTLS {
			return nil, errTLSDowngrade
		}
		if vencryptFailed && cfg.Password != "" && errors.Is(err, errNoSecurityType) {
			return nil, userErr("服务器仅提供匿名 TLS 加密（TLSNone/TLSVnc/TLSPlain），无法验证证书，不受支持")
		}
		return nil, err
	}
	return nil, userErr("VNC 握手失败")
}

func readFull(r io.Reader, n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, userErrWrap("VNC 服务器连接中断", err)
	}
	return buf, nil
}

func readReason(r io.Reader) string {
	head, err := readFull(r, 4)
	if err != nil {
		return ""
	}
	n := binary.BigEndian.Uint32(head)
	if n > 4096 {
		n = 4096
	}
	reason, err := readFull(r, int(n))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(reason))
}

func vncUpstreamHandshake(conn net.Conn, cfg vncUpstreamConfig, allowVeNCrypt bool) (net.Conn, error) {
	banner, err := readFull(conn, 12)
	if err != nil {
		return nil, err
	}
	text := string(banner)
	if !strings.HasPrefix(text, "RFB 003.") || text[11] != '\n' {
		return nil, userErr("目标不是 VNC (RFB) 服务器")
	}
	minor := 0
	if _, err := fmt.Sscanf(text[8:11], "%d", &minor); err != nil {
		return nil, userErr("无法识别 VNC 协议版本")
	}
	version := 8
	switch {
	case minor >= 8:
		version = 8
	case minor >= 7:
		version = 7
	default:
		version = 3
	}
	if _, err := fmt.Fprintf(conn, "RFB 003.%03d\n", version); err != nil {
		return nil, userErrWrap("VNC 服务器连接中断", err)
	}

	var secType uint32
	if version == 3 {
		raw, err := readFull(conn, 4)
		if err != nil {
			return nil, err
		}
		secType = binary.BigEndian.Uint32(raw)
		if secType == 0 {
			return nil, userErrf("VNC 服务器拒绝连接: %s", readReason(conn))
		}
		if cfg.RequireTLS {
			return nil, errTLSDowngrade
		}
		if secType != rfbSecNone && secType != rfbSecVNCAuth {
			return nil, userErrf("不支持的 VNC 安全类型 %d", secType)
		}
	} else {
		count, err := readFull(conn, 1)
		if err != nil {
			return nil, err
		}
		if count[0] == 0 {
			return nil, userErrf("VNC 服务器拒绝连接: %s", readReason(conn))
		}
		types, err := readFull(conn, int(count[0]))
		if err != nil {
			return nil, err
		}
		secType = chooseSecurityType(types, cfg, allowVeNCrypt)
		if cfg.RequireTLS && secType != rfbSecVeNCrypt {
			return nil, errTLSDowngrade
		}
		if secType == 0 {
			return nil, unsupportedSecurity(types, cfg)
		}
		if _, err := conn.Write([]byte{byte(secType)}); err != nil {
			return nil, userErrWrap("VNC 服务器连接中断", err)
		}
	}

	switch secType {
	case rfbSecNone:
		if version >= 8 {
			if err := readSecurityResult(conn, version); err != nil {
				return nil, err
			}
		}
		return conn, nil
	case rfbSecVNCAuth:
		if err := vncAuth(conn, cfg.Password); err != nil {
			return nil, err
		}
		if err := readSecurityResult(conn, version); err != nil {
			return nil, err
		}
		return conn, nil
	case rfbSecARD:
		if err := ardAuth(conn, cfg.Username, cfg.Password); err != nil {
			return nil, err
		}
		if err := readSecurityResult(conn, version); err != nil {
			return nil, err
		}
		return conn, nil
	case rfbSecVeNCrypt:
		return veNCryptAuth(conn, cfg, version)
	}
	return nil, userErrf("不支持的 VNC 安全类型 %d", secType)
}

func chooseSecurityType(offered []byte, cfg vncUpstreamConfig, allowVeNCrypt bool) uint32 {
	has := func(t byte) bool {
		for _, o := range offered {
			if o == t {
				return true
			}
		}
		return false
	}
	hasPassword := cfg.Password != ""
	switch {
	case allowVeNCrypt && has(rfbSecVeNCrypt) && (hasPassword || !has(rfbSecNone) || cfg.RequireTLS):
		return rfbSecVeNCrypt
	case has(rfbSecARD) && hasPassword && cfg.Username != "":
		return rfbSecARD
	case has(rfbSecVNCAuth) && hasPassword:
		return rfbSecVNCAuth
	case has(rfbSecNone):
		return rfbSecNone
	}
	return 0
}

func unsupportedSecurity(offered []byte, cfg vncUpstreamConfig) error {
	if cfg.Password == "" {
		for _, t := range offered {
			if t == rfbSecVNCAuth || t == rfbSecVeNCrypt || t == rfbSecARD {
				return &userError{msg: "VNC 服务器需要密码，请在资产中填写密码", kind: errNoSecurityType}
			}
		}
	}
	names := make([]string, 0, len(offered))
	for _, t := range offered {
		names = append(names, fmt.Sprint(t))
	}
	return &userError{msg: fmt.Sprintf("VNC 服务器仅提供不支持的安全类型（%s），支持 None、VNC 密码、VeNCrypt(X509) 与 Apple ARD", strings.Join(names, ",")), kind: errNoSecurityType}
}

func readSecurityResult(conn io.Reader, version int) error {
	raw, err := readFull(conn, 4)
	if err != nil {
		return err
	}
	if binary.BigEndian.Uint32(raw) == 0 {
		return nil
	}
	if version >= 8 {
		if reason := readReason(conn); reason != "" {
			return userErrf("VNC 认证失败: %s", reason)
		}
	}
	return userErr("VNC 认证失败，请检查用户名或密码")
}

func reverseBits(b byte) byte {
	var out byte
	for i := 0; i < 8; i++ {
		out = out<<1 | (b>>i)&1
	}
	return out
}

// vncChallengeResponse is the classic VNC authentication: DES-ECB with a bit-reversed 8 byte key.
func vncChallengeResponse(challenge []byte, password string) ([]byte, error) {
	key := make([]byte, 8)
	for i := 0; i < 8 && i < len(password); i++ {
		key[i] = reverseBits(password[i])
	}
	block, err := des.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 16)
	block.Encrypt(out[:8], challenge[:8])
	block.Encrypt(out[8:], challenge[8:16])
	return out, nil
}

func vncAuth(conn io.ReadWriter, password string) error {
	challenge, err := readFull(conn, 16)
	if err != nil {
		return err
	}
	response, err := vncChallengeResponse(challenge, password)
	if err != nil {
		return userErrWrap("VNC 密码处理失败", err)
	}
	if _, err := conn.Write(response); err != nil {
		return userErrWrap("VNC 服务器连接中断", err)
	}
	return nil
}

// ardAuth implements Apple Remote Desktop authentication (security type 30): Diffie-Hellman, MD5 of the shared
// secret as AES-128-ECB key, 128 byte credential block.
func ardAuth(conn io.ReadWriter, username, password string) error {
	head, err := readFull(conn, 4)
	if err != nil {
		return err
	}
	generator := int64(binary.BigEndian.Uint16(head[:2]))
	keyLen := int(binary.BigEndian.Uint16(head[2:]))
	if keyLen == 0 || keyLen > 1024 {
		return userErr("Apple ARD 密钥长度无效")
	}
	material, err := readFull(conn, 2*keyLen)
	if err != nil {
		return err
	}
	prime := new(big.Int).SetBytes(material[:keyLen])
	serverPub := new(big.Int).SetBytes(material[keyLen:])
	privRaw := make([]byte, keyLen)
	if _, err := rand.Read(privRaw); err != nil {
		return userErrWrap("生成随机数失败", err)
	}
	priv := new(big.Int).SetBytes(privRaw)
	pub := new(big.Int).Exp(big.NewInt(generator), priv, prime)
	shared := new(big.Int).Exp(serverPub, priv, prime)
	sharedBytes := shared.FillBytes(make([]byte, keyLen))
	key := md5.Sum(sharedBytes)

	credentials := make([]byte, 128)
	if _, err := rand.Read(credentials); err != nil {
		return userErrWrap("生成随机数失败", err)
	}
	copyTerminated := func(dst []byte, s string) {
		if len(s) > len(dst)-1 {
			s = s[:len(dst)-1]
		}
		copy(dst, s)
		dst[len(s)] = 0
	}
	copyTerminated(credentials[:64], username)
	copyTerminated(credentials[64:], password)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return userErrWrap("Apple ARD 加密失败", err)
	}
	encrypted := make([]byte, 128)
	for i := 0; i < 128; i += aes.BlockSize {
		block.Encrypt(encrypted[i:i+aes.BlockSize], credentials[i:i+aes.BlockSize])
	}
	out := append(encrypted, pub.FillBytes(make([]byte, keyLen))...)
	if _, err := conn.Write(out); err != nil {
		return userErrWrap("VNC 服务器连接中断", err)
	}
	return nil
}

func veNCryptAuth(conn net.Conn, cfg vncUpstreamConfig, version int) (net.Conn, error) {
	ver, err := readFull(conn, 2)
	if err != nil {
		return nil, err
	}
	if ver[0] != 0 || ver[1] < 2 {
		return nil, userErrf("不支持的 VeNCrypt 版本 %d.%d", ver[0], ver[1])
	}
	if _, err := conn.Write([]byte{0, 2}); err != nil {
		return nil, userErrWrap("VNC 服务器连接中断", err)
	}
	ack, err := readFull(conn, 1)
	if err != nil {
		return nil, err
	}
	if ack[0] != 0 {
		return nil, userErr("VNC 服务器不接受 VeNCrypt 0.2")
	}
	count, err := readFull(conn, 1)
	if err != nil {
		return nil, err
	}
	raw, err := readFull(conn, int(count[0])*4)
	if err != nil {
		return nil, err
	}
	offered := map[uint32]bool{}
	for i := 0; i+4 <= len(raw); i += 4 {
		offered[binary.BigEndian.Uint32(raw[i:])] = true
	}
	var subtype uint32
	hasUser := cfg.Username != ""
	hasPassword := cfg.Password != ""
	switch {
	case offered[vencryptX509Plain] && hasPassword && hasUser:
		subtype = vencryptX509Plain
	case offered[vencryptX509Vnc] && hasPassword:
		subtype = vencryptX509Vnc
	case offered[vencryptX509Plain] && hasPassword:
		subtype = vencryptX509Plain
	case offered[vencryptX509None]:
		subtype = vencryptX509None
	case offered[vencryptPlain] && hasPassword && hasUser && !cfg.RequireTLS:
		subtype = vencryptPlain
	default:
		return nil, errVeNCryptNoSubtype
	}
	var choice [4]byte
	binary.BigEndian.PutUint32(choice[:], subtype)
	if _, err := conn.Write(choice[:]); err != nil {
		return nil, userErrWrap("VNC 服务器连接中断", err)
	}
	accepted, err := readFull(conn, 1)
	if err != nil {
		return nil, err
	}
	if accepted[0] == 0 {
		return nil, userErr("VNC 服务器拒绝了所选的 VeNCrypt 认证方式")
	}

	secured := conn
	if subtype != vencryptPlain {
		secured, err = upgradeVNCTLS(conn, cfg)
		if err != nil {
			return nil, err
		}
	}
	switch subtype {
	case vencryptX509Plain, vencryptPlain:
		header := make([]byte, 8)
		binary.BigEndian.PutUint32(header[:4], uint32(len(cfg.Username)))
		binary.BigEndian.PutUint32(header[4:], uint32(len(cfg.Password)))
		payload := append(header, cfg.Username...)
		payload = append(payload, cfg.Password...)
		if _, err := secured.Write(payload); err != nil {
			return nil, userErrWrap("VNC 服务器连接中断", err)
		}
	case vencryptX509Vnc:
		if err := vncAuth(secured, cfg.Password); err != nil {
			return nil, err
		}
	}
	// VeNCrypt always ends with a SecurityResult, including the None sub-authentication.
	if err := readSecurityResult(secured, version); err != nil {
		return nil, err
	}
	return secured, nil
}

func upgradeVNCTLS(conn net.Conn, cfg vncUpstreamConfig) (net.Conn, error) {
	var chain [][]byte
	var leaf *x509.Certificate
	tlsConn := tls.Client(conn, &tls.Config{
		MinVersion: tls.VersionTLS12,
		// The certificate is pinned by fingerprint (trust on first use) instead of chain verification.
		InsecureSkipVerify: true,
		ServerName:         sniName(cfg.ServerName),
		VerifyConnection: func(state tls.ConnectionState) error {
			for _, cert := range state.PeerCertificates {
				chain = append(chain, cert.Raw)
			}
			if len(state.PeerCertificates) > 0 {
				leaf = state.PeerCertificates[0]
			}
			return nil
		},
	})
	ctx, cancel := contextWithTimeout(15 * time.Second)
	defer cancel()
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, userErrWrap("VNC TLS 握手失败", err)
	}
	if len(chain) == 0 {
		return nil, userErr("VNC 服务器没有提供证书")
	}
	if cfg.VerifyCert != nil {
		if err := cfg.VerifyCert(chain, leaf); err != nil {
			return nil, err
		}
	}
	return tlsConn, nil
}

func sniName(host string) string {
	if net.ParseIP(host) != nil {
		return ""
	}
	return host
}
