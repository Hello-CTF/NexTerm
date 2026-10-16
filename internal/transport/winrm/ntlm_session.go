package winrm

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rc4"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/Azure/go-ntlmssp"
)

const (
	ntlmNegotiateSign                    = 0x00000010
	ntlmNegotiateSeal                    = 0x00000020
	ntlmNegotiate56                      = 0x80000000
	ntlmNegotiate128                     = 0x20000000
	ntlmNegotiateKeyExch                 = 0x40000000
	ntlmNegotiateExtendedSessionSecurity = 0x00080000
	ntlmClientToServerSigning            = "session key to client-to-server signing key magic constant"
	ntlmClientToServerSealing            = "session key to client-to-server sealing key magic constant"
	ntlmServerToClientSigning            = "session key to server-to-client signing key magic constant"
	ntlmServerToClientSealing            = "session key to server-to-client sealing key magic constant"
	ntlmVersionMagic                     = "\x01\x00\x00\x00"
	ntlmSignatureLength                  = 16
)

func ntlmChallengeNegotiateFlags(challengeToken []byte) (uint32, error) {
	if len(challengeToken) < 24 {
		return 0, fmt.Errorf("ntlmssp: challenge token too short to contain NegotiateFlags: %d bytes", len(challengeToken))
	}
	return binary.LittleEndian.Uint32(challengeToken[20:24]), nil
}

type azureNTLMSecuritySession struct {
	outgoingSeal, incomingSeal             *rc4.Cipher
	outgoingSignKey, incomingSignKey       []byte
	outgoingSeqNum, expectedIncomingSeqNum uint32
	keyExch                                bool
	mu                                     sync.Mutex
}

func newAzureNTLMSecuritySession(sessionKey []byte, negotiateFlags uint32, isClient bool) (*azureNTLMSecuritySession, error) {
	if negotiateFlags&ntlmNegotiateExtendedSessionSecurity == 0 {
		return nil, errors.New("ntlmssp: NTLM1/LM-key mode is not supported (extended session security not negotiated)")
	}
	if negotiateFlags&ntlmNegotiate128 == 0 {
		return nil, errors.New("ntlmssp: server did not negotiate required 128-bit key strength")
	}
	if negotiateFlags&ntlmNegotiateSeal == 0 {
		return nil, errors.New("ntlmssp: server did not grant NTLMSSP_NEGOTIATE_SEAL; message confidentiality unavailable")
	}
	clientSealCipher, err := rc4.NewCipher(ntlmDerivedKey(sessionKey, ntlmClientToServerSealing))
	if err != nil {
		return nil, fmt.Errorf("ntlmssp: creating client seal cipher: %w", err)
	}
	serverSealCipher, err := rc4.NewCipher(ntlmDerivedKey(sessionKey, ntlmServerToClientSealing))
	if err != nil {
		return nil, fmt.Errorf("ntlmssp: creating server seal cipher: %w", err)
	}
	s := &azureNTLMSecuritySession{keyExch: negotiateFlags&ntlmNegotiateKeyExch != 0}
	if isClient {
		s.outgoingSeal, s.incomingSeal = clientSealCipher, serverSealCipher
		s.outgoingSignKey, s.incomingSignKey = deriveClientSignKey(sessionKey), deriveServerSignKey(sessionKey)
	} else {
		s.outgoingSeal, s.incomingSeal = serverSealCipher, clientSealCipher
		s.outgoingSignKey, s.incomingSignKey = deriveServerSignKey(sessionKey), deriveClientSignKey(sessionKey)
	}
	return s, nil
}

func (s *azureNTLMSecuritySession) Wrap(b []byte) ([]byte, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ciphertext, signature := sealMessage(s.outgoingSeal, s.outgoingSignKey, s.outgoingSeqNum, b, s.keyExch)
	s.outgoingSeqNum++
	return ciphertext, signature, nil
}

func (s *azureNTLMSecuritySession) Unwrap(b, signature []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(signature) < ntlmSignatureLength {
		return nil, errors.New("ntlmssp: signature must be at least 16 bytes")
	}
	gotSeqNum := binary.LittleEndian.Uint32(signature[12:ntlmSignatureLength])
	if gotSeqNum != s.expectedIncomingSeqNum {
		return nil, fmt.Errorf("ntlmssp: unexpected sequence number in signature (got %d, want %d)", gotSeqNum, s.expectedIncomingSeqNum)
	}
	plaintext, err := unsealMessage(s.incomingSeal, s.incomingSignKey, signature, b, s.keyExch)
	if err != nil {
		return nil, err
	}
	s.expectedIncomingSeqNum++
	return plaintext, nil
}

func negotiateAzureNTLMSessionKey(username, password string, negotiateChallenge func([]byte) ([]byte, error)) (authenticateToken, sessionKey []byte, negotiateFlags uint32, err error) {
	negotiateToken, err := ntlmssp.NewNegotiateMessageWithOptions(ntlmssp.NegotiateMessageOptions{RequestSealing: true})
	if err != nil {
		return nil, nil, 0, fmt.Errorf("ntlmssp: building NEGOTIATE message: %w", err)
	}
	challengeToken, err := negotiateChallenge(negotiateToken)
	if err != nil {
		return nil, nil, 0, err
	}
	negotiateFlags, err = ntlmChallengeNegotiateFlags(challengeToken)
	if err != nil {
		return nil, nil, 0, err
	}
	patchedChallenge, micRequired, err := ntlmPatchChallengeForMIC(challengeToken)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("ntlmssp: preparing MIC: %w", err)
	}
	var exportedSessionKey []byte
	authenticateToken, err = ntlmssp.NewAuthenticateMessage(patchedChallenge, username, password, &ntlmssp.AuthenticateMessageOptions{
		ExportedSessionKey: &exportedSessionKey,
		RequireSealing:     true,
	})
	if err != nil {
		return nil, nil, 0, fmt.Errorf("ntlmssp: building AUTHENTICATE message: %w", err)
	}
	authenticateToken, err = ntlmInsertVersion(authenticateToken, negotiateFlags)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("ntlmssp: adding Version block: %w", err)
	}
	if micRequired {
		authenticateToken, err = ntlmAttachMIC(negotiateToken, challengeToken, authenticateToken, exportedSessionKey)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("ntlmssp: attaching MIC: %w", err)
		}
	}
	return authenticateToken, exportedSessionKey, negotiateFlags, nil
}

type azureNTLMMessageProtector struct {
	session *azureNTLMSecuritySession
}

func (p azureNTLMMessageProtector) Wrap(message []byte) ([]byte, error) {
	if p.session == nil {
		return nil, errors.New("ntlmssp: NTLM security session is not established")
	}
	sealed, signature, err := p.session.Wrap(message)
	if err != nil {
		return nil, err
	}
	buf := bytes.NewBuffer(make([]byte, 0, 4+len(signature)+len(sealed)))
	if err := binary.Write(buf, binary.LittleEndian, uint32(len(signature))); err != nil {
		return nil, err
	}
	buf.Write(signature)
	buf.Write(sealed)
	return buf.Bytes(), nil
}

func (p azureNTLMMessageProtector) Unwrap(encryptedData []byte, _ int) ([]byte, error) {
	if p.session == nil {
		return nil, errors.New("ntlmssp: NTLM security session is not established")
	}
	if len(encryptedData) < 4 {
		return nil, errors.New("ntlmssp: NTLM encrypted payload is shorter than its length prefix")
	}
	signatureLength := int(binary.LittleEndian.Uint32(encryptedData[:4]))
	if signatureLength < 0 || signatureLength > len(encryptedData)-4 {
		return nil, fmt.Errorf("ntlmssp: invalid NTLM signature length %d for payload of %d bytes", signatureLength, len(encryptedData))
	}
	signature := encryptedData[4 : 4+signatureLength]
	sealed := encryptedData[4+signatureLength:]
	return p.session.Unwrap(sealed, signature)
}

func sealMessage(cipher *rc4.Cipher, signKey []byte, seqNum uint32, plaintext []byte, keyExch bool) (ciphertext, signature []byte) {
	seq := ntlmSeqBytes(seqNum)
	ciphertext = make([]byte, len(plaintext))
	cipher.XORKeyStream(ciphertext, plaintext)
	signature = ntlmSign(cipher, signKey, seq, plaintext, keyExch)
	return ciphertext, signature
}

func unsealMessage(cipher *rc4.Cipher, signKey []byte, signature, ciphertext []byte, keyExch bool) ([]byte, error) {
	if len(signature) != ntlmSignatureLength {
		return nil, errors.New("ntlmssp: signature must be 16 bytes")
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.XORKeyStream(plaintext, ciphertext)
	expected := ntlmSign(cipher, signKey, signature[12:16], plaintext, keyExch)
	if !hmac.Equal(signature, expected) {
		return nil, errors.New("ntlmssp: signature mismatch")
	}
	return plaintext, nil
}

func deriveClientSignKey(sessionKey []byte) []byte {
	return ntlmDerivedKey(sessionKey, ntlmClientToServerSigning)
}

func deriveServerSignKey(sessionKey []byte) []byte {
	return ntlmDerivedKey(sessionKey, ntlmServerToClientSigning)
}

func ntlmDerivedKey(sessionKey []byte, magicConstant string) []byte {
	keyIn := slices.Concat(sessionKey, append([]byte(magicConstant), 0))
	sum := md5.Sum(keyIn)
	return sum[:]
}

func ntlmSeqBytes(seqNum uint32) []byte {
	return []byte{byte(seqNum), byte(seqNum >> 8), byte(seqNum >> 16), byte(seqNum >> 24)}
}

func ntlmSign(sealCipher *rc4.Cipher, signKey []byte, seq []byte, plaintext []byte, keyExch bool) []byte {
	checksum := ntlmHmacMd5(signKey, append(seq, plaintext...))[:8]
	if !keyExch {
		return append(append([]byte(ntlmVersionMagic), checksum...), seq...)
	}
	encHmac := make([]byte, 8)
	sealCipher.XORKeyStream(encHmac, checksum)
	return append(append([]byte(ntlmVersionMagic), encHmac...), seq...)
}

func ntlmHmacMd5(key []byte, data ...[]byte) []byte {
	h := hmac.New(md5.New, key)
	for _, d := range data {
		h.Write(d)
	}
	return h.Sum(nil)
}
