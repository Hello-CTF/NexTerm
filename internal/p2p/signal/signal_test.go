package signal

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestFramesRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ca, cb := NewConn(a), NewConn(b)
	frames := []Frame{
		{Type: TypeOffer, Grant: "g", ClientPub: "k", Ufrag: "uf", Pwd: "pw", NAT: "independent"},
		{Type: TypeCandidate, Candidate: "candidate:1 1 udp 1 127.0.0.1 5000 typ host"},
		{Type: TypePing, Seq: 7},
	}
	go func() {
		for _, frame := range frames {
			if err := ca.Write(context.Background(), frame); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i, want := range frames {
		got, err := cb.Read()
		if err != nil || got != want {
			t.Fatalf("frame %d = %+v %v, want %+v", i, got, err, want)
		}
	}
}

func TestReadRejectsOversizedAndUntypedFrames(t *testing.T) {
	for name, message := range map[string][]byte{
		"oversized": binary.BigEndian.AppendUint32(nil, MaxFrame+1),
		"empty":     binary.BigEndian.AppendUint32(nil, 0),
		"untyped":   append(binary.BigEndian.AppendUint32(nil, 2), "{}"...),
	} {
		a, b := net.Pipe()
		go func() { _, _ = a.Write(message); _ = a.Close() }()
		if _, err := NewConn(b).Read(); err == nil {
			t.Errorf("%s frame accepted", name)
		}
		_ = b.Close()
	}
}

func TestWriteHonorsContext(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := NewConn(a).Write(ctx, Frame{Type: TypePing}); err == nil {
		t.Fatal("write without a reader succeeded")
	}
}

func TestAnswerSignature(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(nil)
	other, _, _ := ed25519.GenerateKey(nil)
	answer := Frame{Type: TypeAnswer, Ufrag: "agent-ufrag", Pwd: "agent-pwd", NAT: "dependent"}
	answer.AgentPubSig = AnswerSignature(private, "grant-1", "client-ufrag", answer)
	if !VerifyAnswer(public, "grant-1", "client-ufrag", answer) {
		t.Fatal("valid answer refused")
	}
	if VerifyAnswer(other, "grant-1", "client-ufrag", answer) {
		t.Fatal("answer accepted under another key")
	}
	if VerifyAnswer(public, "grant-2", "client-ufrag", answer) || VerifyAnswer(public, "grant-1", "other", answer) {
		t.Fatal("answer accepted for another grant or offer")
	}
	tampered := answer
	tampered.Pwd = "changed"
	if VerifyAnswer(public, "grant-1", "client-ufrag", tampered) {
		t.Fatal("tampered answer accepted")
	}
}

func TestPurposePreamble(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() { _ = WritePurpose(a, "file-mux") }()
	if purpose, err := ReadPurpose(b); err != nil || purpose != "file-mux" {
		t.Fatalf("ReadPurpose = %q %v", purpose, err)
	}
	if err := WritePurpose(a, ""); err == nil {
		t.Fatal("empty purpose written")
	}
	go func() { _, _ = a.Write([]byte{0}) }()
	if _, err := ReadPurpose(b); err == nil {
		t.Fatal("empty purpose read")
	}
}
