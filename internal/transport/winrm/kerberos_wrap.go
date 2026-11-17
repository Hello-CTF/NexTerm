package winrm

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/jcmturner/gokrb5/v8/crypto"
	"github.com/jcmturner/gokrb5/v8/crypto/etype"
	"github.com/jcmturner/gokrb5/v8/iana/etypeID"
	"github.com/jcmturner/gokrb5/v8/types"
)

// RFC 4121 section 4.2 wrap tokens with confidentiality, in the layout WinRM frames as
// <uint32 LE header length><header><encrypted data>: the header is the 16-byte token header followed by the
// rotated part of the ciphertext, the encrypted data has the length of the plaintext.
const (
	kerberosWrapTokenID           = 0x0504
	kerberosWrapHeaderLength      = 16
	kerberosWrapSentByAcceptor    = 0x01
	kerberosWrapSealed            = 0x02
	kerberosWrapAcceptorSubkey    = 0x04
	kerberosKeyUsageAcceptorSeal  = 22
	kerberosKeyUsageInitiatorSeal = 24
)

type kerberosMessageProtector struct {
	mu             sync.Mutex
	key            types.EncryptionKey
	etype          etype.EType
	initiator      bool
	acceptorSubkey bool
	sendSeq        uint64
	recvSeq        uint64
}

// newKerberosMessageProtector accepts only the AES enctypes Windows offers; RC4-HMAC uses a different
// (RFC 4757) token format that is not implemented.
func newKerberosMessageProtector(key types.EncryptionKey, initiator, acceptorSubkey bool, sendSeq, recvSeq uint64) (*kerberosMessageProtector, error) {
	switch key.KeyType {
	case etypeID.AES128_CTS_HMAC_SHA1_96, etypeID.AES256_CTS_HMAC_SHA1_96:
	default:
		return nil, fmt.Errorf("Kerberos 会话密钥的加密类型 %d 不支持 WinRM 消息加密（仅支持 AES）", key.KeyType)
	}
	et, err := crypto.GetEtype(key.KeyType)
	if err != nil {
		return nil, err
	}
	if len(key.KeyValue) != et.GetKeyByteSize() {
		return nil, fmt.Errorf("kerberos: session key has %d bytes, want %d", len(key.KeyValue), et.GetKeyByteSize())
	}
	return &kerberosMessageProtector{
		key:            key,
		etype:          et,
		initiator:      initiator,
		acceptorSubkey: acceptorSubkey,
		sendSeq:        sendSeq,
		recvSeq:        recvSeq,
	}, nil
}

func (p *kerberosMessageProtector) usages() (send, receive uint32) {
	if p.initiator {
		return kerberosKeyUsageInitiatorSeal, kerberosKeyUsageAcceptorSeal
	}
	return kerberosKeyUsageAcceptorSeal, kerberosKeyUsageInitiatorSeal
}

func (p *kerberosMessageProtector) flags(fromAcceptor bool) byte {
	flags := byte(kerberosWrapSealed)
	if fromAcceptor {
		flags |= kerberosWrapSentByAcceptor
	}
	if p.acceptorSubkey {
		flags |= kerberosWrapAcceptorSubkey
	}
	return flags
}

func kerberosWrapHeader(flags byte, ec, rrc uint16, seq uint64) []byte {
	header := make([]byte, kerberosWrapHeaderLength)
	binary.BigEndian.PutUint16(header[0:2], kerberosWrapTokenID)
	header[2] = flags
	header[3] = 0xFF
	binary.BigEndian.PutUint16(header[4:6], ec)
	binary.BigEndian.PutUint16(header[6:8], rrc)
	binary.BigEndian.PutUint64(header[8:16], seq)
	return header
}

func (p *kerberosMessageProtector) Wrap(message []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	sendUsage, _ := p.usages()
	// AES-CTS needs no padding, so EC is 0 and the encrypted header copy keeps RRC 0 (RFC 4121 section 4.2.4).
	header := kerberosWrapHeader(p.flags(!p.initiator), 0, 0, p.sendSeq)
	plaintext := make([]byte, 0, len(message)+kerberosWrapHeaderLength)
	plaintext = append(plaintext, message...)
	plaintext = append(plaintext, header...)
	_, ciphertext, err := p.etype.EncryptMessage(p.key.KeyValue, plaintext, sendUsage)
	if err != nil {
		return nil, fmt.Errorf("kerberos: encrypt wrap token: %w", err)
	}
	// Rotate the encrypted header copy and the checksum to the front, so the ciphertext of the message itself
	// is the trailing part of the token.
	rrc := kerberosWrapHeaderLength + p.etype.GetHMACBitLength()/8
	binary.BigEndian.PutUint16(header[6:8], uint16(rrc))
	rotated := rotateRight(ciphertext, rrc)
	headerLength := kerberosWrapHeaderLength + len(rotated) - len(message)
	var framed bytes.Buffer
	framed.Grow(4 + kerberosWrapHeaderLength + len(rotated))
	if err := binary.Write(&framed, binary.LittleEndian, uint32(headerLength)); err != nil {
		return nil, err
	}
	framed.Write(header)
	framed.Write(rotated)
	p.sendSeq++
	return framed.Bytes(), nil
}

func (p *kerberosMessageProtector) Unwrap(encryptedData []byte, _ int) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(encryptedData) < 4 {
		return nil, errors.New("kerberos: encrypted payload is shorter than its length prefix")
	}
	headerLength := int(binary.LittleEndian.Uint32(encryptedData[:4]))
	token := encryptedData[4:]
	if headerLength < kerberosWrapHeaderLength || headerLength > len(token) {
		return nil, fmt.Errorf("kerberos: invalid wrap header length %d for payload of %d bytes", headerLength, len(token))
	}
	header := token[:kerberosWrapHeaderLength]
	if binary.BigEndian.Uint16(header[0:2]) != kerberosWrapTokenID || header[3] != 0xFF {
		return nil, errors.New("kerberos: response is not a wrap token")
	}
	flags := header[2]
	if flags&kerberosWrapSealed == 0 {
		return nil, errors.New("kerberos: response wrap token is not encrypted")
	}
	if fromAcceptor := flags&kerberosWrapSentByAcceptor != 0; fromAcceptor != p.initiator {
		return nil, errors.New("kerberos: response wrap token has the wrong direction")
	}
	if (flags&kerberosWrapAcceptorSubkey != 0) != p.acceptorSubkey {
		return nil, errors.New("kerberos: response wrap token uses a different key than the security context")
	}
	ec := int(binary.BigEndian.Uint16(header[4:6]))
	rrc := int(binary.BigEndian.Uint16(header[6:8]))
	seq := binary.BigEndian.Uint64(header[8:16])
	if seq != p.recvSeq {
		return nil, fmt.Errorf("kerberos: unexpected sequence number (got %d, want %d)", seq, p.recvSeq)
	}
	ciphertext := rotateLeft(token[kerberosWrapHeaderLength:], rrc)
	_, receiveUsage := p.usages()
	if len(ciphertext) < p.etype.GetConfounderByteSize()+p.etype.GetHMACBitLength()/8 {
		return nil, errors.New("kerberos: wrap token is too short")
	}
	plaintext, err := p.etype.DecryptMessage(p.key.KeyValue, ciphertext, receiveUsage)
	if err != nil {
		return nil, fmt.Errorf("kerberos: decrypt wrap token: %w", err)
	}
	if len(plaintext) < ec+kerberosWrapHeaderLength {
		return nil, errors.New("kerberos: decrypted wrap token is too short")
	}
	headerCopy := plaintext[len(plaintext)-kerberosWrapHeaderLength:]
	expected := append([]byte(nil), header...)
	binary.BigEndian.PutUint16(expected[6:8], 0)
	if !bytes.Equal(headerCopy, expected) {
		return nil, errors.New("kerberos: wrap token header does not match its encrypted copy")
	}
	p.recvSeq++
	return plaintext[:len(plaintext)-ec-kerberosWrapHeaderLength], nil
}

func rotateRight(data []byte, count int) []byte {
	if len(data) == 0 {
		return append([]byte(nil), data...)
	}
	count %= len(data)
	rotated := make([]byte, 0, len(data))
	rotated = append(rotated, data[len(data)-count:]...)
	return append(rotated, data[:len(data)-count]...)
}

func rotateLeft(data []byte, count int) []byte {
	if len(data) == 0 {
		return append([]byte(nil), data...)
	}
	count %= len(data)
	rotated := make([]byte, 0, len(data))
	rotated = append(rotated, data[count:]...)
	return append(rotated, data[:count]...)
}
