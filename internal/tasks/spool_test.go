package tasks

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

func sequence(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte('a' + i%26)
	}
	return out
}

func TestSpoolHeadTailAndGap(t *testing.T) {
	dir := t.TempDir()
	s, err := createSpool(dir, "s1", 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("abcdefghij")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("klmnopqrst")); err != nil {
		t.Fatal(err)
	}
	out, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Head) != "abcd" || string(out.Tail) != "qrst" || out.Total != 20 || out.Dropped != 12 {
		t.Fatalf("unexpected snapshot: %+v", out)
	}
	head, err := s.Read(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if string(head.Data) != "abcd" || head.Next != 4 || head.Gap {
		t.Fatalf("unexpected head read: %+v", head)
	}
	gap, err := s.Read(5, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !gap.Gap || gap.Offset != 16 || string(gap.Data) != "qrst" || gap.Next != 20 {
		t.Fatalf("unexpected gap read: %+v", gap)
	}
	one, err := s.Read(18, 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(one.Data) != "s" || one.Gap {
		t.Fatalf("unexpected tail read: %+v", one)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("x")); err == nil {
		t.Fatal("write after close must fail")
	}

	reopened, err := openSpool(dir, "s1", 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out2.Head, out.Head) || !bytes.Equal(out2.Tail, out.Tail) ||
		out2.Total != out.Total || out2.Dropped != out.Dropped {
		t.Fatalf("reopen changed the stream: before=%+v after=%+v", out, out2)
	}
}

func TestSpoolCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	s, err := createSpool(dir, "s2", 4, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(sequence(20)); err != nil {
		t.Fatal(err)
	}
	reopened, err := openSpool(dir, "s2", 4, 8)
	if err != nil {
		t.Fatal(err)
	}
	out, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 20 || out.Dropped != 0 || string(out.Head) != "abcd" ||
		!bytes.Equal(out.Tail, sequence(20)[4:]) {
		t.Fatalf("uncheckpointed recovery failed: %+v", out)
	}

	s2, err := createSpool(dir, "s3", 4, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Write(sequence(40)); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Write(sequence(44)[40:]); err != nil {
		t.Fatal(err)
	}
	reopened2, err := openSpool(dir, "s3", 4, 8)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := reopened2.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if out2.Total != 44 || out2.Dropped != 28 || string(out2.Head) != "abcd" ||
		!bytes.Equal(out2.Tail, sequence(44)[32:]) {
		t.Fatalf("post-compaction recovery failed: total=%d dropped=%d head=%q tail=%q",
			out2.Total, out2.Dropped, out2.Head, out2.Tail)
	}
}

func TestSpoolRemoveFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := createSpool(dir, "s4", 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("hello world")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	removeSpoolFiles(dir, "s4")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("spool files not fully removed: %v", entries)
	}
	if _, err := openSpool(dir, "missing", 4, 4); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing spool should read as empty, got %v", err)
	}
}
