package remotedesktop

import (
	"errors"
	"fmt"
)

// RDCleanPath PDU as defined by crates/ironrdp-rdcleanpath (Devolutions IronRDP): a DER SEQUENCE whose fields use
// EXPLICIT context tags [0]..[9]. Peers using the derived decoder reject unknown fields, so none are added here.
const (
	rdCleanPathVersion1 = 3390

	// Error codes (RDCleanPathErr.error_code).
	rdCleanPathGeneralError     = 1
	rdCleanPathNegotiationError = 2

	derSequence    = 0x30
	derInteger     = 0x02
	derOctetString = 0x04
	derUTF8String  = 0x0c
)

func ctxTag(n int) byte { return 0xa0 | byte(n) }

// rdCleanPathPDU holds the fields of a request or response. Pointer-free optionality is expressed with presence flags.
type rdCleanPathPDU struct {
	Version        uint64
	Error          *rdCleanPathError
	Destination    string
	HasDestination bool
	ProxyAuth      string
	HasProxyAuth   bool
	PreconnBlob    string
	HasPreconnBlob bool
	X224           []byte
	HasX224        bool
	CertChain      [][]byte
	HasCertChain   bool
	ServerAddr     string
	HasServerAddr  bool
}

type rdCleanPathError struct {
	Code       uint16
	HTTPStatus uint16
	HasHTTP    bool
	WSAError   uint16
	HasWSA     bool
	TLSAlert   uint8
	HasTLS     bool
}

func derLen(n int) []byte {
	switch {
	case n < 0x80:
		return []byte{byte(n)}
	case n < 0x100:
		return []byte{0x81, byte(n)}
	case n < 0x10000:
		return []byte{0x82, byte(n >> 8), byte(n)}
	case n < 0x1000000:
		return []byte{0x83, byte(n >> 16), byte(n >> 8), byte(n)}
	default:
		return []byte{0x84, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
}

func derTLV(tag byte, content []byte) []byte {
	out := append([]byte{tag}, derLen(len(content))...)
	return append(out, content...)
}

func derUint(v uint64) []byte {
	var raw []byte
	for x := v; ; x >>= 8 {
		raw = append([]byte{byte(x)}, raw...)
		if x < 0x100 {
			break
		}
	}
	if raw[0]&0x80 != 0 {
		raw = append([]byte{0}, raw...)
	}
	return derTLV(derInteger, raw)
}

func (e *rdCleanPathError) marshal() []byte {
	body := derTLV(ctxTag(0), derUint(uint64(e.Code)))
	if e.HasHTTP {
		body = append(body, derTLV(ctxTag(1), derUint(uint64(e.HTTPStatus)))...)
	}
	if e.HasWSA {
		body = append(body, derTLV(ctxTag(2), derUint(uint64(e.WSAError)))...)
	}
	if e.HasTLS {
		body = append(body, derTLV(ctxTag(3), derUint(uint64(e.TLSAlert)))...)
	}
	return derTLV(derSequence, body)
}

func (p *rdCleanPathPDU) marshal() []byte {
	body := derTLV(ctxTag(0), derUint(p.Version))
	if p.Error != nil {
		body = append(body, derTLV(ctxTag(1), p.Error.marshal())...)
	}
	if p.HasDestination {
		body = append(body, derTLV(ctxTag(2), derTLV(derUTF8String, []byte(p.Destination)))...)
	}
	if p.HasProxyAuth {
		body = append(body, derTLV(ctxTag(3), derTLV(derUTF8String, []byte(p.ProxyAuth)))...)
	}
	if p.HasPreconnBlob {
		body = append(body, derTLV(ctxTag(5), derTLV(derUTF8String, []byte(p.PreconnBlob)))...)
	}
	if p.HasX224 {
		body = append(body, derTLV(ctxTag(6), derTLV(derOctetString, p.X224))...)
	}
	if p.HasCertChain {
		var chain []byte
		for _, cert := range p.CertChain {
			chain = append(chain, derTLV(derOctetString, cert)...)
		}
		body = append(body, derTLV(ctxTag(7), derTLV(derSequence, chain))...)
	}
	if p.HasServerAddr {
		body = append(body, derTLV(ctxTag(9), derTLV(derUTF8String, []byte(p.ServerAddr)))...)
	}
	return derTLV(derSequence, body)
}

var errDER = errors.New("RDCleanPath DER 格式无效")

// derRead returns tag, content and the remaining bytes of the first TLV in data.
func derRead(data []byte) (tag byte, content, rest []byte, err error) {
	if len(data) < 2 {
		return 0, nil, nil, errDER
	}
	tag = data[0]
	if tag&0x1f == 0x1f {
		return 0, nil, nil, errDER
	}
	length := int(data[1])
	offset := 2
	if length&0x80 != 0 {
		count := length & 0x7f
		if count == 0 || count > 4 || len(data) < 2+count {
			return 0, nil, nil, errDER
		}
		length = 0
		for _, b := range data[2 : 2+count] {
			length = length<<8 | int(b)
		}
		offset = 2 + count
	}
	if length < 0 || len(data)-offset < length {
		return 0, nil, nil, errDER
	}
	return tag, data[offset : offset+length], data[offset+length:], nil
}

func derExpect(data []byte, want byte) (content, rest []byte, err error) {
	tag, content, rest, err := derRead(data)
	if err != nil {
		return nil, nil, err
	}
	if tag != want {
		return nil, nil, fmt.Errorf("%w: 期望标签 0x%02x，实际 0x%02x", errDER, want, tag)
	}
	return content, rest, nil
}

func derParseUint(content []byte) (uint64, error) {
	if len(content) == 0 || len(content) > 9 || content[0]&0x80 != 0 {
		return 0, errDER
	}
	var v uint64
	for _, b := range content {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

func parseExplicitUint(content []byte) (uint64, error) {
	inner, _, err := derExpect(content, derInteger)
	if err != nil {
		return 0, err
	}
	return derParseUint(inner)
}

func parseExplicitString(content []byte) (string, error) {
	inner, _, err := derExpect(content, derUTF8String)
	if err != nil {
		return "", err
	}
	return string(inner), nil
}

func parseRDCleanPathError(content []byte) (*rdCleanPathError, error) {
	body, _, err := derExpect(content, derSequence)
	if err != nil {
		return nil, err
	}
	out := &rdCleanPathError{}
	for len(body) > 0 {
		tag, inner, rest, err := derRead(body)
		if err != nil {
			return nil, err
		}
		body = rest
		v, err := parseExplicitUint(inner)
		if err != nil {
			return nil, err
		}
		switch tag {
		case ctxTag(0):
			out.Code = uint16(v)
		case ctxTag(1):
			out.HTTPStatus, out.HasHTTP = uint16(v), true
		case ctxTag(2):
			out.WSAError, out.HasWSA = uint16(v), true
		case ctxTag(3):
			out.TLSAlert, out.HasTLS = uint8(v), true
		default:
			return nil, errDER
		}
	}
	return out, nil
}

func parseRDCleanPath(data []byte) (*rdCleanPathPDU, error) {
	body, rest, err := derExpect(data, derSequence)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, errDER
	}
	pdu := &rdCleanPathPDU{}
	haveVersion := false
	for len(body) > 0 {
		tag, inner, next, err := derRead(body)
		if err != nil {
			return nil, err
		}
		body = next
		switch tag {
		case ctxTag(0):
			if pdu.Version, err = parseExplicitUint(inner); err != nil {
				return nil, err
			}
			haveVersion = true
		case ctxTag(1):
			if pdu.Error, err = parseRDCleanPathError(inner); err != nil {
				return nil, err
			}
		case ctxTag(2):
			pdu.Destination, err = parseExplicitString(inner)
			pdu.HasDestination = true
		case ctxTag(3):
			pdu.ProxyAuth, err = parseExplicitString(inner)
			pdu.HasProxyAuth = true
		case ctxTag(4):
			_, err = parseExplicitString(inner) // server_auth: unused
		case ctxTag(5):
			pdu.PreconnBlob, err = parseExplicitString(inner)
			pdu.HasPreconnBlob = true
		case ctxTag(6):
			var octets []byte
			octets, _, err = derExpect(inner, derOctetString)
			pdu.X224, pdu.HasX224 = octets, true
		case ctxTag(7):
			pdu.HasCertChain = true
			var seq []byte
			if seq, _, err = derExpect(inner, derSequence); err != nil {
				return nil, err
			}
			for len(seq) > 0 {
				var cert []byte
				if cert, seq, err = derExpect(seq, derOctetString); err != nil {
					return nil, err
				}
				pdu.CertChain = append(pdu.CertChain, cert)
			}
		case ctxTag(9):
			pdu.ServerAddr, err = parseExplicitString(inner)
			pdu.HasServerAddr = true
		default:
			return nil, errDER
		}
		if err != nil {
			return nil, err
		}
	}
	if !haveVersion {
		return nil, errDER
	}
	return pdu, nil
}

func newRDCleanPathResponse(serverAddr string, x224 []byte, chain [][]byte) []byte {
	return (&rdCleanPathPDU{
		Version: rdCleanPathVersion1, X224: x224, HasX224: true, CertChain: chain, HasCertChain: true,
		ServerAddr: serverAddr, HasServerAddr: true,
	}).marshal()
}

func newRDCleanPathHTTPError(status uint16) []byte {
	return (&rdCleanPathPDU{Version: rdCleanPathVersion1, Error: &rdCleanPathError{Code: rdCleanPathGeneralError, HTTPStatus: status, HasHTTP: true}}).marshal()
}

func newRDCleanPathWSAError(wsa uint16) []byte {
	return (&rdCleanPathPDU{Version: rdCleanPathVersion1, Error: &rdCleanPathError{Code: rdCleanPathGeneralError, WSAError: wsa, HasWSA: true}}).marshal()
}

func newRDCleanPathGeneralError() []byte {
	return (&rdCleanPathPDU{Version: rdCleanPathVersion1, Error: &rdCleanPathError{Code: rdCleanPathGeneralError}}).marshal()
}

func newRDCleanPathNegotiationError(x224 []byte) []byte {
	return (&rdCleanPathPDU{Version: rdCleanPathVersion1, Error: &rdCleanPathError{Code: rdCleanPathNegotiationError}, X224: x224, HasX224: len(x224) > 0}).marshal()
}
