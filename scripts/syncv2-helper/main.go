// syncv2-helper provides the v2 sync protocol's real cryptographic primitives
// for scripts/e2e-harness, letting the acceptance harness encrypt/decrypt
// account DEK envelopes and object envelopes as a standalone binary.
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/Hello-CTF/NexTerm/internal/vault"
)

const (
	objectAADPrefix = "nexterm/go/sync-object/v1"
	objectNonceSize = 12
)

const (
	// compressedKind mirrors wireKindTranscriptGzip in internal/sync/object_compress.go; the sealed plaintext is
	// "NXZ1" | uint64 BE decoded size | gzip stream. The older "transcript.zstd" label is not supported here.
	compressedKind   = "transcript.gzip"
	compressedMagic  = "NXZ1"
	compressedHeader = 12
	maxDecodedBytes  = 96 << 20
)

func packGzip(plaintext []byte) []byte {
	var out bytes.Buffer
	out.WriteString(compressedMagic)
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(plaintext)))
	out.Write(size[:])
	writer := gzip.NewWriter(&out)
	if _, err := writer.Write(plaintext); err != nil {
		fatal(err)
	}
	if err := writer.Close(); err != nil {
		fatal(err)
	}
	return out.Bytes()
}

func unpackGzip(packed []byte) ([]byte, error) {
	if len(packed) <= compressedHeader || string(packed[:len(compressedMagic)]) != compressedMagic {
		return nil, fmt.Errorf("invalid compressed object")
	}
	declared := binary.BigEndian.Uint64(packed[len(compressedMagic):compressedHeader])
	if declared == 0 || declared > maxDecodedBytes {
		return nil, fmt.Errorf("invalid compressed object size")
	}
	reader, err := gzip.NewReader(bytes.NewReader(packed[compressedHeader:]))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	out, err := io.ReadAll(io.LimitReader(reader, int64(declared)+1))
	if err != nil || uint64(len(out)) != declared {
		return nil, fmt.Errorf("invalid compressed object body")
	}
	return out, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "syncv2-helper:", err)
	os.Exit(1)
}

func genDEK(password string) {
	dek, envelopes, _, err := vault.GenerateUserDEKEnvelopes(password)
	if err != nil {
		fatal(err)
	}
	out := map[string]string{
		"dek":               hex.EncodeToString(dek),
		"dek_envelope":      base64.StdEncoding.EncodeToString(envelopes.DEKEnvelope),
		"kdf_salt":          base64.StdEncoding.EncodeToString(envelopes.KDFSalt),
		"kdf_params":        envelopes.KDFParams,
		"recovery_envelope": base64.StdEncoding.EncodeToString(envelopes.RecoveryEnvelope),
		"recovery_hash":     envelopes.RecoveryHash,
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		fatal(err)
	}
	fmt.Println(string(encoded))
}

func objectAAD(id, kind string) []byte {
	return []byte(objectAADPrefix + "\x00" + id + "\x00" + kind)
}

func seal(dekHex, id, kind string) {
	dek, err := hex.DecodeString(dekHex)
	if err != nil || len(dek) != 32 {
		fatal(fmt.Errorf("DEK must be a 32-byte hexadecimal string"))
	}
	plaintext, err := io.ReadAll(os.Stdin)
	if err != nil {
		fatal(err)
	}
	if kind == compressedKind {
		plaintext = packGzip(plaintext)
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		fatal(err)
	}
	nonce := make([]byte, objectNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		fatal(err)
	}
	blob := aead.Seal(nonce, nonce, plaintext, objectAAD(id, kind))
	fmt.Println(base64.StdEncoding.EncodeToString(blob))
}

func open(dekHex, id, kind string) {
	dek, err := hex.DecodeString(dekHex)
	if err != nil || len(dek) != 32 {
		fatal(fmt.Errorf("DEK must be a 32-byte hexadecimal string"))
	}
	encoded, err := io.ReadAll(os.Stdin)
	if err != nil {
		fatal(err)
	}
	blob, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		fatal(err)
	}
	if len(blob) <= objectNonceSize {
		fatal(fmt.Errorf("invalid blob length"))
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		fatal(err)
	}
	plaintext, err := aead.Open(nil, blob[:objectNonceSize], blob[objectNonceSize:], objectAAD(id, kind))
	if err != nil {
		fatal(err)
	}
	if kind == compressedKind {
		if plaintext, err = unpackGzip(plaintext); err != nil {
			fatal(err)
		}
	}
	fmt.Print(string(plaintext))
}

func main() {
	if len(os.Args) < 2 {
		fatal(fmt.Errorf("usage: syncv2-helper <gen-dek|seal|open> ..."))
	}
	switch os.Args[1] {
	case "gen-dek":
		if len(os.Args) != 3 {
			fatal(fmt.Errorf("usage: syncv2-helper gen-dek <password>"))
		}
		genDEK(os.Args[2])
	case "seal":
		if len(os.Args) != 5 {
			fatal(fmt.Errorf("usage: syncv2-helper seal <dek-hex> <id> <kind>"))
		}
		seal(os.Args[2], os.Args[3], os.Args[4])
	case "open":
		if len(os.Args) != 5 {
			fatal(fmt.Errorf("usage: syncv2-helper open <dek-hex> <id> <kind>"))
		}
		open(os.Args[2], os.Args[3], os.Args[4])
	default:
		fatal(fmt.Errorf("unknown subcommand %s", os.Args[1]))
	}
}
