package keys

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"fmt"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	gossh "golang.org/x/crypto/ssh"
)

type Algorithm string

const (
	AlgorithmEd25519 Algorithm = "ed25519"
	AlgorithmRSA     Algorithm = "rsa"
)

const DefaultRSABits = 4096

type Options struct {
	Algorithm  Algorithm
	RSABits    int
	Passphrase string
}

type KeyPair struct {
	Algorithm     Algorithm
	PrivateKeyPEM []byte
	PublicKeyLine string
	Fingerprint   string
}

func Generate(opts Options) (*KeyPair, error) {
	algorithm := opts.Algorithm
	if algorithm == "" {
		algorithm = AlgorithmEd25519
	}
	var (
		signer crypto.Signer
		err    error
	)
	switch algorithm {
	case AlgorithmEd25519:
		if opts.RSABits != 0 {
			return nil, ipc.NewError(ipc.CodeBadParam, "生成密钥失败: ed25519 不支持 RSA 位数参数")
		}
		_, signer, err = ed25519.GenerateKey(rand.Reader)
	case AlgorithmRSA:
		bits := opts.RSABits
		if bits == 0 {
			bits = DefaultRSABits
		}
		if bits != DefaultRSABits {
			return nil, ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("生成密钥失败: 不支持的 RSA 位数 %d", bits))
		}
		var rsaKey *rsa.PrivateKey
		rsaKey, err = rsa.GenerateKey(rand.Reader, bits)
		signer = rsaKey
	default:
		return nil, ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("生成密钥失败: 不支持的算法 %q", algorithm))
	}
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "生成密钥失败: 底层密钥生成错误", err)
	}

	var block *pem.Block
	if opts.Passphrase == "" {
		block, err = gossh.MarshalPrivateKey(signer, "nexterm")
	} else {
		block, err = gossh.MarshalPrivateKeyWithPassphrase(signer, "nexterm", []byte(opts.Passphrase))
	}
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "生成密钥失败: 私钥编码错误", err)
	}
	publicKey, err := gossh.NewPublicKey(signer.Public())
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "生成密钥失败: 公钥编码错误", err)
	}
	return &KeyPair{
		Algorithm:     algorithm,
		PrivateKeyPEM: pem.EncodeToMemory(block),
		PublicKeyLine: strings.TrimSpace(string(gossh.MarshalAuthorizedKey(publicKey))),
		Fingerprint:   gossh.FingerprintSHA256(publicKey),
	}, nil
}
