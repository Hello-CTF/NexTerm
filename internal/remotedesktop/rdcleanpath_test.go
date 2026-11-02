package remotedesktop

import (
	"bytes"
	"encoding/asn1"
	"encoding/hex"
	"testing"
)

// The structures below describe the RDCleanPath PDU independently of the hand written codec: encoding/asn1 is the
// oracle for the layout of crates/ironrdp-rdcleanpath (SEQUENCE with EXPLICIT context tags [0]..[9]).
type oracleRequest struct {
	Version     int    `asn1:"explicit,tag:0"`
	Destination string `asn1:"explicit,tag:2,optional,utf8"`
	ProxyAuth   string `asn1:"explicit,tag:3,optional,utf8"`
	PreconnBlob string `asn1:"explicit,tag:5,optional,utf8"`
	X224        []byte `asn1:"explicit,tag:6,optional"`
}

type oracleResponse struct {
	Version    int      `asn1:"explicit,tag:0"`
	X224       []byte   `asn1:"explicit,tag:6,optional"`
	CertChain  [][]byte `asn1:"explicit,tag:7,optional"`
	ServerAddr string   `asn1:"explicit,tag:9,optional,utf8"`
}

func TestRDCleanPathRequestMatchesOracle(t *testing.T) {
	x224 := bytes.Repeat([]byte{0x03, 0x00}, 150) // forces a long-form DER length
	oracle, err := asn1.Marshal(oracleRequest{Version: 3390, Destination: "evil.example:22", ProxyAuth: "ticket-1", PreconnBlob: "blob", X224: x224})
	if err != nil {
		t.Fatal(err)
	}
	pdu, err := parseRDCleanPath(oracle)
	if err != nil {
		t.Fatalf("parse oracle bytes: %v", err)
	}
	if pdu.Version != rdCleanPathVersion1 || pdu.Destination != "evil.example:22" || pdu.ProxyAuth != "ticket-1" || pdu.PreconnBlob != "blob" || !bytes.Equal(pdu.X224, x224) {
		t.Fatalf("parsed = %+v", pdu)
	}
	if got := pdu.marshal(); !bytes.Equal(got, oracle) {
		t.Fatalf("marshal differs from oracle\n got %x\nwant %x", got, oracle)
	}
}

func TestRDCleanPathResponseMatchesOracle(t *testing.T) {
	chain := [][]byte{bytes.Repeat([]byte{1}, 700), bytes.Repeat([]byte{2}, 20)}
	x224 := []byte{3, 0, 0, 19, 14, 0xd0}
	got := newRDCleanPathResponse("10.0.0.5:3389", x224, chain)
	var decoded oracleResponse
	if rest, err := asn1.Unmarshal(got, &decoded); err != nil || len(rest) != 0 {
		t.Fatalf("oracle decode: %v rest=%d", err, len(rest))
	}
	if decoded.Version != 3390 || !bytes.Equal(decoded.X224, x224) || decoded.ServerAddr != "10.0.0.5:3389" || len(decoded.CertChain) != 2 || !bytes.Equal(decoded.CertChain[0], chain[0]) {
		t.Fatalf("decoded = %+v", decoded)
	}
	oracle, err := asn1.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(oracle, got) {
		t.Fatalf("encoding differs from oracle\n got %x\nwant %x", got, oracle)
	}
	back, err := parseRDCleanPath(got)
	if err != nil || len(back.CertChain) != 2 || !bytes.Equal(back.CertChain[1], chain[1]) || back.ServerAddr != "10.0.0.5:3389" {
		t.Fatalf("round trip = %+v, %v", back, err)
	}
}

func TestRDCleanPathErrorVector(t *testing.T) {
	// SEQUENCE { [0] 3390, [1] SEQUENCE { [0] 1, [1] 403 } } written out by hand from the ASN.1 module.
	want, _ := hex.DecodeString("3015a00402020d3ea10d300ba003020101a10402020193")
	if got := newRDCleanPathHTTPError(403); !bytes.Equal(got, want) {
		t.Fatalf("http error = %x, want %x", got, want)
	}
	pdu, err := parseRDCleanPath(want)
	if err != nil || pdu.Error == nil || pdu.Error.Code != 1 || !pdu.Error.HasHTTP || pdu.Error.HTTPStatus != 403 {
		t.Fatalf("parsed = %+v, %v", pdu, err)
	}
	wsa, err := parseRDCleanPath(newRDCleanPathWSAError(10061))
	if err != nil || !wsa.Error.HasWSA || wsa.Error.WSAError != 10061 {
		t.Fatalf("wsa = %+v, %v", wsa, err)
	}
	neg, err := parseRDCleanPath(newRDCleanPathNegotiationError([]byte{1, 2, 3}))
	if err != nil || neg.Error.Code != rdCleanPathNegotiationError || !bytes.Equal(neg.X224, []byte{1, 2, 3}) {
		t.Fatalf("negotiation = %+v, %v", neg, err)
	}
}

func TestRDCleanPathRejectsMalformed(t *testing.T) {
	good := (&rdCleanPathPDU{Version: 3390, X224: []byte{1}, HasX224: true}).marshal()
	for name, data := range map[string][]byte{
		"empty":         nil,
		"truncated":     good[:len(good)-1],
		"trailing":      append(append([]byte(nil), good...), 0),
		"not sequence":  {0x04, 0x01, 0x00},
		"no version":    {0x30, 0x00},
		"unknown field": {0x30, 0x08, 0xa0, 0x04, 0x02, 0x02, 0x0d, 0x3e, 0xaf, 0x00},
	} {
		if _, err := parseRDCleanPath(data); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
}
