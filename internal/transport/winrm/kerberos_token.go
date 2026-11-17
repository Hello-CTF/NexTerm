package winrm

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jcmturner/gofork/encoding/asn1"
	"github.com/jcmturner/gokrb5/v8/asn1tools"
	"github.com/jcmturner/gokrb5/v8/crypto"
	"github.com/jcmturner/gokrb5/v8/gssapi"
	"github.com/jcmturner/gokrb5/v8/iana/chksumtype"
	"github.com/jcmturner/gokrb5/v8/iana/flags"
	"github.com/jcmturner/gokrb5/v8/iana/keyusage"
	"github.com/jcmturner/gokrb5/v8/messages"
	"github.com/jcmturner/gokrb5/v8/spnego"
	"github.com/jcmturner/gokrb5/v8/types"
)

const (
	krb5TokenAPReq    = "0100"
	krb5TokenAPRep    = "0200"
	krb5TokenKRBError = "0300"
	// Mutual authentication delivers the AP-REP that carries the acceptor's sequence number and subkey; message
	// encryption needs confidentiality, integrity and sequencing.
	kerberosContextFlags = gssapi.ContextFlagMutual | gssapi.ContextFlagReplay | gssapi.ContextFlagSequence |
		gssapi.ContextFlagConf | gssapi.ContextFlagInteg
)

// kerberosTicket is a service ticket for the WinRM SPN with the client identity it was issued to.
type kerberosTicket struct {
	ticket     messages.Ticket
	sessionKey types.EncryptionKey
	realm      string
	cname      types.PrincipalName
}

// kerberosInitiator is the client side of one security context: the AP-REQ it sent, so the AP-REP can be checked.
type kerberosInitiator struct {
	sessionKey    types.EncryptionKey
	authenticator types.Authenticator
}

// newKerberosInitiator builds the SPNEGO NegTokenInit carrying a mutual-auth AP-REQ with a fresh subkey and
// sequence number.
func newKerberosInitiator(ticket kerberosTicket) (*kerberosInitiator, []byte, error) {
	et, err := crypto.GetEtype(ticket.sessionKey.KeyType)
	if err != nil {
		return nil, nil, fmt.Errorf("kerberos: session key type: %w", err)
	}
	authenticator, err := types.NewAuthenticator(ticket.realm, ticket.cname)
	if err != nil {
		return nil, nil, fmt.Errorf("kerberos: build authenticator: %w", err)
	}
	random := make([]byte, 4+et.GetKeyByteSize())
	if _, err := rand.Read(random); err != nil {
		return nil, nil, fmt.Errorf("kerberos: generate subkey: %w", err)
	}
	authenticator.SeqNumber = int64(binary.BigEndian.Uint32(random[:4]) & 0x3fffffff)
	authenticator.SubKey = types.EncryptionKey{KeyType: ticket.sessionKey.KeyType, KeyValue: et.RandomToKey(random[4:])}
	authenticator.Cksum = types.Checksum{CksumType: chksumtype.GSSAPI, Checksum: gssapiAuthenticatorChecksum(kerberosContextFlags)}
	apReq, err := messages.NewAPReq(ticket.ticket, ticket.sessionKey, authenticator)
	if err != nil {
		return nil, nil, fmt.Errorf("kerberos: build AP-REQ: %w", err)
	}
	types.SetFlag(&apReq.APOptions, flags.APOptionMutualRequired)
	apReqBytes, err := apReq.Marshal()
	if err != nil {
		return nil, nil, fmt.Errorf("kerberos: marshal AP-REQ: %w", err)
	}
	mechToken, err := marshalKRB5Token(krb5TokenAPReq, apReqBytes)
	if err != nil {
		return nil, nil, err
	}
	init := spnego.SPNEGOToken{Init: true, NegTokenInit: spnego.NegTokenInit{
		MechTypes:      []asn1.ObjectIdentifier{gssapi.OIDKRB5.OID()},
		MechTokenBytes: mechToken,
	}}
	token, err := init.Marshal()
	if err != nil {
		return nil, nil, fmt.Errorf("kerberos: marshal SPNEGO token: %w", err)
	}
	return &kerberosInitiator{sessionKey: ticket.sessionKey, authenticator: authenticator}, token, nil
}

// gssapiAuthenticatorChecksum is the RFC 4121 section 4.1.1 checksum: no channel bindings, the context flags.
func gssapiAuthenticatorChecksum(contextFlags uint32) []byte {
	checksum := make([]byte, 24)
	binary.LittleEndian.PutUint32(checksum[0:4], 16)
	binary.LittleEndian.PutUint32(checksum[20:24], contextFlags)
	return checksum
}

func marshalKRB5Token(tokenID string, body []byte) ([]byte, error) {
	oid, err := asn1.Marshal(gssapi.OIDKRB5.OID())
	if err != nil {
		return nil, fmt.Errorf("kerberos: marshal mechanism OID: %w", err)
	}
	id, err := hex.DecodeString(tokenID)
	if err != nil {
		return nil, err
	}
	token := append(append(oid, id...), body...)
	return asn1tools.AddASNAppTag(token, 0), nil
}

// parseKRB5Token reads an RFC 1964 framed Kerberos token, accepting the standard and the Microsoft mechanism OID.
func parseKRB5Token(b []byte) (tokenID string, body []byte, err error) {
	var oid asn1.ObjectIdentifier
	rest, err := asn1.UnmarshalWithParams(b, &oid, "application,explicit,tag:0")
	if err != nil {
		return "", nil, fmt.Errorf("kerberos: read mechanism token: %w", err)
	}
	if !oid.Equal(gssapi.OIDKRB5.OID()) && !oid.Equal(gssapi.OIDMSLegacyKRB5.OID()) {
		return "", nil, fmt.Errorf("kerberos: unexpected mechanism %s in response", oid)
	}
	if len(rest) < 2 {
		return "", nil, errors.New("kerberos: mechanism token is too short")
	}
	return hex.EncodeToString(rest[:2]), rest[2:], nil
}

// kerberosResponseMechToken extracts the Kerberos mechanism token from an HTTP Negotiate response token, which is a
// SPNEGO NegTokenResp or, from some servers, the raw Kerberos token.
func kerberosResponseMechToken(token []byte) ([]byte, error) {
	isInit, negotiation, err := spnego.UnmarshalNegToken(token)
	if err != nil {
		return token, nil
	}
	if isInit {
		return nil, errors.New("kerberos: server answered with a SPNEGO init token")
	}
	response, ok := negotiation.(spnego.NegTokenResp)
	if !ok {
		return nil, errors.New("kerberos: unexpected SPNEGO response")
	}
	if response.State() == spnego.NegStateReject {
		return response.ResponseToken, errKerberosRejected
	}
	return response.ResponseToken, nil
}

var errKerberosRejected = errors.New("kerberos: server rejected the security context")

// complete verifies the server's mutual-auth response and returns the message protector for the context.
func (i *kerberosInitiator) complete(responseToken []byte) (*kerberosMessageProtector, error) {
	mechToken, rejected := kerberosResponseMechToken(responseToken)
	if len(mechToken) == 0 {
		if rejected != nil {
			return nil, newKerberosError(kerberosFailureRejected, rejected)
		}
		return nil, newKerberosError(kerberosFailureNoMutualAuth, errors.New("kerberos: response has no AP-REP"))
	}
	tokenID, body, err := parseKRB5Token(mechToken)
	if err != nil {
		return nil, err
	}
	switch tokenID {
	case krb5TokenKRBError:
		var krbError messages.KRBError
		if err := krbError.Unmarshal(body); err != nil {
			return nil, fmt.Errorf("kerberos: read KRB-ERROR: %w", err)
		}
		return nil, classifyKerberosError(krbError)
	case krb5TokenAPRep:
	default:
		return nil, fmt.Errorf("kerberos: unexpected token %s in response", tokenID)
	}
	var apRep messages.APRep
	if err := apRep.Unmarshal(body); err != nil {
		return nil, fmt.Errorf("kerberos: read AP-REP: %w", err)
	}
	plain, err := crypto.DecryptEncPart(apRep.EncPart, i.sessionKey, keyusage.AP_REP_ENCPART)
	if err != nil {
		return nil, newKerberosError(kerberosFailureMutualAuth, fmt.Errorf("kerberos: decrypt AP-REP: %w", err))
	}
	var part messages.EncAPRepPart
	if err := part.Unmarshal(plain); err != nil {
		return nil, fmt.Errorf("kerberos: read AP-REP: %w", err)
	}
	if part.CTime.Unix() != i.authenticator.CTime.Unix() || part.Cusec != i.authenticator.Cusec {
		return nil, newKerberosError(kerberosFailureMutualAuth, errors.New("kerberos: AP-REP does not answer this AP-REQ"))
	}
	key, acceptorSubkey := i.authenticator.SubKey, false
	if len(part.Subkey.KeyValue) != 0 {
		key, acceptorSubkey = part.Subkey, true
	}
	return newKerberosMessageProtector(key, true, acceptorSubkey, uint64(i.authenticator.SeqNumber), uint64(uint32(part.SequenceNumber)))
}
