package store

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/klauspost/compress/zstd"
)

const (
	transcriptStoredBlockKind = -1
	transcriptBlockMaxItems   = 256
	transcriptBlockMaxBytes   = 4 << 20
	transcriptBlockMaxDecoded = 96 << 20
)

var transcriptBlockMagic = []byte("NXT1")

func encodeTranscriptChunks(chunks []TranscriptChunkRow) ([]TranscriptChunkRow, error) {
	if len(chunks) == 0 {
		return nil, nil
	}
	ordered := append([]TranscriptChunkRow(nil), chunks...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })
	for index := 1; index < len(ordered); index++ {
		if ordered[index].Seq == ordered[index-1].Seq {
			return nil, badParam(fmt.Errorf("transcript chunk sequence numbers must be unique"))
		}
	}

	var blocks []TranscriptChunkRow
	for start := 0; start < len(ordered); {
		end := start + 1
		rawBytes := len(ordered[start].Data) + len(ordered[start].TabID)
		if rawBytes > transcriptBlockMaxDecoded {
			return nil, badParam(fmt.Errorf("transcript chunk exceeds the maximum decodable size"))
		}
		for end < len(ordered) && end-start < transcriptBlockMaxItems && rawBytes+len(ordered[end].Data)+len(ordered[end].TabID) <= transcriptBlockMaxBytes {
			rawBytes += len(ordered[end].Data) + len(ordered[end].TabID)
			end++
		}
		data, err := encodeTranscriptBlock(ordered[start:end])
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, TranscriptChunkRow{
			Seq: ordered[end-1].Seq, TS: ordered[end-1].TS, Kind: transcriptStoredBlockKind, Data: data,
		})
		start = end
	}
	return blocks, nil
}

// transcriptEncoders reuses zstd encoders: building one allocates well over a megabyte of window and
// history buffers, which made every transcript flush a large garbage-collection burden.
var transcriptEncoders sync.Pool

func acquireTranscriptEncoder(out io.Writer) (*zstd.Encoder, error) {
	if pooled, ok := transcriptEncoders.Get().(*zstd.Encoder); ok {
		pooled.Reset(out)
		return pooled, nil
	}
	return zstd.NewWriter(out, zstd.WithWindowSize(1<<20), zstd.WithEncoderConcurrency(1))
}

func encodeTranscriptBlock(chunks []TranscriptChunkRow) ([]byte, error) {
	var compressed bytes.Buffer
	writer, err := acquireTranscriptEncoder(&compressed)
	if err != nil {
		return nil, err
	}
	// Only a cleanly closed encoder goes back to the pool; one that failed mid-block is discarded.
	closeWithError := func(err error) ([]byte, error) {
		_ = writer.Close()
		return nil, err
	}
	if err := transcriptWrite(writer, transcriptBlockMagic); err != nil {
		return closeWithError(err)
	}
	if err := transcriptWriteUvarint(writer, uint64(len(chunks))); err != nil {
		return closeWithError(err)
	}
	for _, chunk := range chunks {
		if err := transcriptWriteVarint(writer, chunk.Seq); err != nil {
			return closeWithError(err)
		}
		if err := transcriptWriteVarint(writer, chunk.TS); err != nil {
			return closeWithError(err)
		}
		if err := transcriptWriteVarint(writer, int64(chunk.Kind)); err != nil {
			return closeWithError(err)
		}
		if err := transcriptWriteUvarint(writer, uint64(len(chunk.TabID))); err != nil {
			return closeWithError(err)
		}
		if err := transcriptWrite(writer, []byte(chunk.TabID)); err != nil {
			return closeWithError(err)
		}
		if err := transcriptWriteUvarint(writer, uint64(len(chunk.Data))); err != nil {
			return closeWithError(err)
		}
		if err := transcriptWrite(writer, chunk.Data); err != nil {
			return closeWithError(err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	writer.Reset(nil) // drop the reference to this block's output buffer before pooling
	transcriptEncoders.Put(writer)
	return compressed.Bytes(), nil
}

func decodeTranscriptBlock(stored TranscriptChunkRow) ([]TranscriptChunkRow, error) {
	if stored.Kind != transcriptStoredBlockKind {
		return nil, transcriptFormatError(nil)
	}
	reader, err := zstd.NewReader(bytes.NewReader(stored.Data), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(transcriptBlockMaxDecoded))
	if err != nil {
		return nil, transcriptFormatError(err)
	}
	defer reader.Close()
	buffered := bufio.NewReader(reader)
	magic := make([]byte, len(transcriptBlockMagic))
	if _, err := io.ReadFull(buffered, magic); err != nil {
		return nil, transcriptFormatError(err)
	}
	if !bytes.Equal(magic, transcriptBlockMagic) {
		return nil, transcriptFormatError(nil)
	}
	count, err := binary.ReadUvarint(buffered)
	if err != nil || count == 0 || count > transcriptBlockMaxItems {
		return nil, transcriptFormatError(err)
	}
	chunks := make([]TranscriptChunkRow, 0, int(count))
	decodedBytes := 0
	for index := 0; index < int(count); index++ {
		seq, err := binary.ReadVarint(buffered)
		if err != nil {
			return nil, transcriptFormatError(err)
		}
		ts, err := binary.ReadVarint(buffered)
		if err != nil {
			return nil, transcriptFormatError(err)
		}
		kind, err := binary.ReadVarint(buffered)
		if err != nil {
			return nil, transcriptFormatError(err)
		}
		tabLength, err := binary.ReadUvarint(buffered)
		if err != nil || tabLength > uint64(transcriptBlockMaxDecoded-decodedBytes) {
			return nil, transcriptFormatError(err)
		}
		tab := make([]byte, int(tabLength))
		if _, err := io.ReadFull(buffered, tab); err != nil {
			return nil, transcriptFormatError(err)
		}
		decodedBytes += len(tab)
		dataLength, err := binary.ReadUvarint(buffered)
		if err != nil || dataLength > uint64(transcriptBlockMaxDecoded-decodedBytes) {
			return nil, transcriptFormatError(err)
		}
		data := make([]byte, int(dataLength))
		if _, err := io.ReadFull(buffered, data); err != nil {
			return nil, transcriptFormatError(err)
		}
		decodedBytes += len(data)
		if index > 0 && seq <= chunks[index-1].Seq {
			return nil, transcriptFormatError(fmt.Errorf("transcript chunk sequence is not increasing"))
		}
		chunks = append(chunks, TranscriptChunkRow{
			TranscriptID: stored.TranscriptID, Seq: seq, TabID: string(tab), TS: ts,
			Kind: int(kind), Data: data, OwnerID: stored.OwnerID,
		})
	}
	if chunks[len(chunks)-1].Seq != stored.Seq {
		return nil, transcriptFormatError(fmt.Errorf("transcript block sequence does not match its index"))
	}
	if _, err := buffered.ReadByte(); err != io.EOF {
		return nil, transcriptFormatError(fmt.Errorf("transcript block has trailing data"))
	}
	return chunks, nil
}

func transcriptWriteVarint(writer io.Writer, value int64) error {
	var buffer [binary.MaxVarintLen64]byte
	n := binary.PutVarint(buffer[:], value)
	return transcriptWrite(writer, buffer[:n])
}

func transcriptWriteUvarint(writer io.Writer, value uint64) error {
	var buffer [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buffer[:], value)
	return transcriptWrite(writer, buffer[:n])
}

func transcriptWrite(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func transcriptFormatError(err error) error {
	const message = "会话记录格式不受支持或数据已损坏"
	if err == nil {
		return ipc.NewError(ipc.CodeUnsupported, message)
	}
	return ipc.WrapError(ipc.CodeUnsupported, message, err)
}
