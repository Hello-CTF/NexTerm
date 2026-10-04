package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

type spool struct {
	mu        sync.Mutex
	dir       string
	base      string
	headLimit int64
	tailLimit int64
	head      *os.File
	tail      *os.File
	total     int64
	headLen   int64
	tailStart int64
	tailLen   int64
	closed    bool
	crashHook func(stage string)
}

type spoolIndex struct {
	Total    int64 `json:"total"`
	HeadSize int64 `json:"headSize"`
	TailSize int64 `json:"tailSize"`
}

func (i spoolIndex) valid() bool {
	return i.Total >= 0 && i.HeadSize >= 0 && i.TailSize >= 0 && i.Total >= i.HeadSize+i.TailSize
}

func (s *spool) headPath() string    { return filepath.Join(s.dir, s.base+".head") }
func (s *spool) tailPath() string    { return filepath.Join(s.dir, s.base+".tail") }
func (s *spool) indexPath() string   { return filepath.Join(s.dir, s.base+".idx") }
func (s *spool) compactPath() string { return filepath.Join(s.dir, s.base+".compact") }

func writeJSONAtomic(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readSpoolIndex(path string) *spoolIndex {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var idx spoolIndex
	if json.Unmarshal(raw, &idx) != nil || !idx.valid() {
		return nil
	}
	return &idx
}

func createSpool(dir, base string, headLimit, tailLimit int64) (*spool, error) {
	s := &spool{dir: dir, base: base, headLimit: headLimit, tailLimit: tailLimit}
	head, err := os.OpenFile(s.headPath(), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	tail, err := os.OpenFile(s.tailPath(), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		head.Close()
		os.Remove(s.headPath())
		return nil, err
	}
	s.head, s.tail = head, tail
	if err := s.writeIndexLocked(); err != nil {
		head.Close()
		tail.Close()
		removeSpoolFiles(dir, base)
		return nil, err
	}
	return s, nil
}

func openSpool(dir, base string, headLimit, tailLimit int64) (*spool, error) {
	s := &spool{dir: dir, base: base, headLimit: headLimit, tailLimit: tailLimit, closed: true}
	var headSize, tailSize int64
	if st, err := os.Stat(s.headPath()); err == nil {
		headSize = st.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if st, err := os.Stat(s.tailPath()); err == nil {
		tailSize = st.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	idx := readSpoolIndex(s.indexPath())
	journal := readSpoolIndex(s.compactPath())
	adopted := false
	keepTransaction := false
	var repairErr error
	if journal != nil && headSize == journal.HeadSize {
		if tailSize == journal.TailSize {
			adopted = true
		} else if st, err := os.Stat(s.tailPath() + ".tmp"); err == nil && st.Size() == journal.TailSize {
			if err := os.Rename(s.tailPath()+".tmp", s.tailPath()); err == nil {
				adopted = true
			} else {
				keepTransaction = true
				repairErr = fmt.Errorf("tasks: replay compaction for %s: %w", base, err)
			}
		}
	}
	if adopted {
		s.headLen = headSize
		s.tailLen = journal.TailSize
		s.total = journal.Total
		s.tailStart = journal.Total - journal.TailSize
		if err := s.writeIndexLocked(); err != nil {
			return s, fmt.Errorf("tasks: repair checkpoint for %s: %w", base, err)
		}
		_ = os.Remove(s.compactPath())
		_ = os.Remove(s.tailPath() + ".tmp")
		return s, nil
	}
	if !keepTransaction {
		_ = os.Remove(s.compactPath())
		_ = os.Remove(s.tailPath() + ".tmp")
	}
	s.headLen = headSize
	s.tailLen = tailSize
	s.total = headSize + tailSize
	s.tailStart = headSize
	if idx != nil && headSize >= idx.HeadSize && tailSize >= idx.TailSize {
		s.total = idx.Total + (headSize - idx.HeadSize) + (tailSize - idx.TailSize)
		s.tailStart = s.total - tailSize
	}
	if s.tailStart < s.headLen {
		s.tailStart = s.headLen
	}
	if s.total < s.headLen+s.tailLen {
		s.total = s.headLen + s.tailLen
	}
	return s, repairErr
}

func (s *spool) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.head == nil || s.tail == nil {
		return 0, errors.New("task spool is closed")
	}
	n := len(p)
	rest := p
	if room := s.headLimit - s.headLen; room > 0 && len(rest) > 0 {
		chunk := rest
		if int64(len(chunk)) > room {
			chunk = chunk[:room]
		}
		if _, err := s.head.WriteAt(chunk, s.headLen); err != nil {
			return 0, err
		}
		s.headLen += int64(len(chunk))
		rest = rest[len(chunk):]
	}
	if len(rest) > 0 {
		if _, err := s.tail.WriteAt(rest, s.tailLen); err != nil {
			return 0, err
		}
		s.tailLen += int64(len(rest))
	}
	s.total += int64(n)
	s.tailStart = s.total - s.tailLen
	if s.tailLen > 2*s.tailLimit {
		if err := s.compactLocked(); err != nil {
			return n, err
		}
	}
	return n, nil
}

func (s *spool) compactLocked() error {
	if s.tailLen <= s.tailLimit {
		return nil
	}
	keep := s.tailLimit
	buf := make([]byte, keep)
	if _, err := s.tail.ReadAt(buf, s.tailLen-keep); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	tmp := s.tailPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	s.crash("tmp")
	journal := spoolIndex{Total: s.total, HeadSize: s.headLen, TailSize: keep}
	if err := writeJSONAtomic(s.compactPath(), journal); err != nil {
		return err
	}
	s.crash("journal")
	if err := s.tail.Close(); err != nil {
		return err
	}
	s.tail = nil
	if err := os.Rename(tmp, s.tailPath()); err != nil {
		return err
	}
	s.crash("rename")
	tail, err := os.OpenFile(s.tailPath(), os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	s.tail = tail
	s.tailLen = keep
	s.tailStart = s.total - keep
	if err := s.writeIndexLocked(); err != nil {
		return err
	}
	s.crash("index")
	return os.Remove(s.compactPath())
}

func (s *spool) crash(stage string) {
	if s.crashHook != nil {
		s.crashHook(stage)
	}
}

var spoolIndexWriter = faultHook[func(path string, value any) error]{fn: writeJSONAtomic}

func (s *spool) writeIndexLocked() error {
	return spoolIndexWriter.get()(s.indexPath(), spoolIndex{Total: s.total, HeadSize: s.headLen, TailSize: s.tailLen})
}

func (s *spool) Stats() (total, dropped int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total, s.droppedLocked()
}

func (s *spool) droppedLocked() int64 {
	dropped := s.tailStart - s.headLen
	if dropped < 0 {
		return 0
	}
	return dropped
}

func (s *spool) Snapshot() (Output, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head, err := s.readRegion(true, 0, s.headLen)
	if err != nil {
		return Output{}, err
	}
	tail, err := s.readRegion(false, 0, s.tailLen)
	if err != nil {
		return Output{}, err
	}
	return Output{Head: head, Tail: tail, Total: s.total, Dropped: s.droppedLocked()}, nil
}

func (s *spool) Read(offset int64, max int) (Chunk, error) {
	if offset < 0 {
		return Chunk{}, ErrInvalidOffset
	}
	if max <= 0 {
		max = DefaultHeadBytes
	}
	if max > maxReadBytes {
		max = maxReadBytes
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	chunk := Chunk{Total: s.total}
	if offset >= s.total {
		chunk.Offset = s.total
		chunk.Next = s.total
		return chunk, nil
	}
	if offset < s.headLen {
		n := int64(max)
		if remain := s.headLen - offset; n > remain {
			n = remain
		}
		data, err := s.readRegion(true, offset, n)
		if err != nil {
			return Chunk{}, err
		}
		chunk.Data = data
		chunk.Offset = offset
		chunk.Next = offset + int64(len(data))
		return chunk, nil
	}
	if offset < s.tailStart {
		offset = s.tailStart
		chunk.Gap = true
	}
	fileOffset := offset - s.tailStart
	n := int64(max)
	if remain := s.tailLen - fileOffset; n > remain {
		n = remain
	}
	data, err := s.readRegion(false, fileOffset, n)
	if err != nil {
		return Chunk{}, err
	}
	chunk.Data = data
	chunk.Offset = offset
	chunk.Next = offset + int64(len(data))
	return chunk, nil
}

func (s *spool) readRegion(head bool, offset, n int64) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	f, path := s.tail, s.tailPath()
	if head {
		f, path = s.head, s.headPath()
	}
	if f == nil {
		var err error
		f, err = os.Open(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, nil
			}
			return nil, err
		}
		defer f.Close()
	}
	buf := make([]byte, n)
	read, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:read], nil
}

func (s *spool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var firstErr error
	if s.tail != nil && s.tailLen > s.tailLimit {
		if err := s.compactLocked(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := s.writeIndexLocked(); err != nil && firstErr == nil {
		firstErr = err
	}
	for _, f := range []*os.File{s.head, s.tail} {
		if f != nil {
			if err := f.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	s.head, s.tail = nil, nil
	return firstErr
}

func removeSpoolFiles(dir, base string) {
	for _, suffix := range []string{".head", ".tail", ".idx", ".compact", ".tail.tmp", ".idx.tmp", ".compact.tmp"} {
		os.Remove(filepath.Join(dir, base+suffix))
	}
}
