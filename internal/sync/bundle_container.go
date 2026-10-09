package sync

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"golang.org/x/crypto/argon2"
)

const (
	bundleMagic              = "NXBM"
	bundleVersion       byte = 1
	bundleHeaderSize         = 64
	bundleFlagEncrypted      = 1
	bundleSaltSize           = 16
	bundleNonceSize          = 12
	bundleKeySize            = 32
	bundleGCMTagSize         = 16

	bundleMaxKDFTime    = 64
	bundleMaxKDFMemory  = 1 << 20
	bundleMaxKDFThreads = 64
)

type bundleKDFParams struct {
	Time      uint32
	MemoryKiB uint32
	Threads   uint32
}

var bundleKDFDefaults = bundleKDFParams{Time: 3, MemoryKiB: 64 * 1024, Threads: 4}

type bundleHeader struct {
	encrypted bool
	kdf       bundleKDFParams
	salt      [bundleSaltSize]byte
	nonce     [bundleNonceSize]byte
	plainLen  uint64
	cipherLen uint64
}

func (h *bundleHeader) marshal() []byte {
	buf := make([]byte, bundleHeaderSize)
	copy(buf[0:4], bundleMagic)
	buf[4] = bundleVersion
	if h.encrypted {
		buf[5] = bundleFlagEncrypted
	}
	binary.BigEndian.PutUint32(buf[6:10], h.kdf.Time)
	binary.BigEndian.PutUint32(buf[10:14], h.kdf.MemoryKiB)
	binary.BigEndian.PutUint32(buf[14:18], h.kdf.Threads)
	buf[18] = bundleSaltSize
	copy(buf[19:35], h.salt[:])
	buf[35] = bundleNonceSize
	copy(buf[36:48], h.nonce[:])
	binary.BigEndian.PutUint64(buf[48:56], h.plainLen)
	binary.BigEndian.PutUint64(buf[56:64], h.cipherLen)
	return buf
}

func parseBundleHeader(data []byte) (*bundleHeader, error) {
	if len(data) < bundleHeaderSize || string(data[0:4]) != bundleMagic || data[4] != bundleVersion {
		return nil, ipc.NewError(ipc.CodeBadParam, "资产包头不合法")
	}
	h := &bundleHeader{
		encrypted: data[5]&bundleFlagEncrypted != 0,
		kdf: bundleKDFParams{
			Time:      binary.BigEndian.Uint32(data[6:10]),
			MemoryKiB: binary.BigEndian.Uint32(data[10:14]),
			Threads:   binary.BigEndian.Uint32(data[14:18]),
		},
		plainLen:  binary.BigEndian.Uint64(data[48:56]),
		cipherLen: binary.BigEndian.Uint64(data[56:64]),
	}
	if data[18] != bundleSaltSize || data[35] != bundleNonceSize {
		return nil, ipc.NewError(ipc.CodeBadParam, "资产包头不合法")
	}
	copy(h.salt[:], data[19:35])
	copy(h.nonce[:], data[36:48])
	if h.encrypted {
		if h.kdf.Time == 0 || h.kdf.Time > bundleMaxKDFTime ||
			h.kdf.MemoryKiB < 8*1024 || h.kdf.MemoryKiB > bundleMaxKDFMemory ||
			h.kdf.Threads == 0 || h.kdf.Threads > bundleMaxKDFThreads {
			return nil, ipc.NewError(ipc.CodeBadParam, "资产包 KDF 参数越界")
		}
		if h.cipherLen != h.plainLen+bundleGCMTagSize {
			return nil, ipc.NewError(ipc.CodeBadParam, "资产包长度字段不一致")
		}
	}
	return h, nil
}

func deriveBundleKey(password string, salt []byte, kdf bundleKDFParams) []byte {
	return argon2.IDKey([]byte(password), salt, kdf.Time, kdf.MemoryKiB, uint8(kdf.Threads), bundleKeySize)
}

func sealBundle(plaintext []byte, password string, header *bundleHeader) ([]byte, error) {
	key := deriveBundleKey(password, header.salt[:], header.kdf)
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ipc.NewError(ipc.CodeCrypto, "资产包加密失败")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ipc.NewError(ipc.CodeCrypto, "资产包加密失败")
	}
	return aead.Seal(nil, header.nonce[:], plaintext, header.marshal()), nil
}

func encodeBundleContainer(plaintext []byte, password string) ([]byte, error) {
	header := &bundleHeader{plainLen: uint64(len(plaintext))}
	if password == "" {
		return append(header.marshal(), plaintext...), nil
	}
	header.encrypted = true
	header.kdf = bundleKDFDefaults
	header.cipherLen = header.plainLen + bundleGCMTagSize
	if _, err := rand.Read(header.salt[:]); err != nil {
		return nil, ipc.NewError(ipc.CodeCrypto, "资产包加密失败")
	}
	if _, err := rand.Read(header.nonce[:]); err != nil {
		return nil, ipc.NewError(ipc.CodeCrypto, "资产包加密失败")
	}
	ciphertext, err := sealBundle(plaintext, password, header)
	if err != nil {
		return nil, err
	}
	return append(header.marshal(), ciphertext...), nil
}

func decodeBundleContainer(data []byte, password string) ([]byte, error) {
	header, err := parseBundleHeader(data)
	if err != nil {
		return nil, err
	}
	body := data[bundleHeaderSize:]
	if !header.encrypted {
		if uint64(len(body)) != header.plainLen {
			return nil, ipc.NewError(ipc.CodeBadParam, "资产包长度字段不一致")
		}
		return body, nil
	}
	if uint64(len(body)) != header.cipherLen {
		return nil, ipc.NewError(ipc.CodeBadParam, "资产包数据不完整")
	}
	key := deriveBundleKey(password, header.salt[:], header.kdf)
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ipc.NewError(ipc.CodeCrypto, "资产包解密失败")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ipc.NewError(ipc.CodeCrypto, "资产包解密失败")
	}
	plaintext, err := aead.Open(nil, header.nonce[:], body, data[:bundleHeaderSize])
	if err != nil {
		if password == "" {
			return nil, ipc.NewError(ipc.CodeBadParam, "资产包已加密，请提供口令")
		}
		return nil, ipc.NewError(ipc.CodeDecrypt, "资产包解密失败：口令错误或数据已被篡改")
	}
	if uint64(len(plaintext)) != header.plainLen {
		return nil, ipc.NewError(ipc.CodeDecrypt, "资产包解密失败：口令错误或数据已被篡改")
	}
	return plaintext, nil
}
