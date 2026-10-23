package main

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	localHeaderSignature   = 0x04034b50
	centralHeaderSignature = 0x02014b50
	endRecordSignature     = 0x06054b50
	maxZipEntries          = 100000
)

type zipEntry struct {
	name           string
	flags          uint16
	method         uint16
	crc            uint32
	compressedSize int64
	size           int64
	externalAttrs  uint32
	headerOffset   int64
	dataStart      int64
}

func (entry zipEntry) mode() uint32     { return (entry.externalAttrs >> 16) & 0o177777 }
func (entry zipEntry) isDir() bool      { return strings.HasSuffix(entry.name, "/") }
func (entry zipEntry) fileType() uint32 { return entry.mode() & 0o170000 }

const (
	typeRegular   = 0o100000
	typeDirectory = 0o040000
)

type requiredEntry struct {
	Mode uint32 `json:"mode"`
	Size int64  `json:"size"`
}

type zipManifest struct {
	entryCount             int
	totalUncompressedBytes int64
	required               map[string]requiredEntry
	entries                map[string]zipEntry
}

// inspectZip validates the central directory before any entry content is read:
// unique safe names, regular files and directories only, declared size limits,
// and local headers that match the central directory without letting any
// entry's compressed data reach into another header.
func inspectZip(file *os.File, size, maxEntry, maxTotal int64, requiredNames []string) (*zipManifest, error) {
	centralStart, centralSize, count, err := readEndRecord(file, size)
	if err != nil {
		return nil, err
	}
	if count > maxZipEntries {
		return nil, fmt.Errorf("ZIP has more than %d entries", maxZipEntries)
	}
	central := make([]byte, centralSize)
	if _, err := file.ReadAt(central, centralStart); err != nil {
		return nil, fmt.Errorf("cannot read the ZIP central directory: %v", err)
	}
	required := map[string]bool{}
	for _, name := range requiredNames {
		required[name] = true
	}
	manifest := &zipManifest{required: map[string]requiredEntry{}, entries: map[string]zipEntry{}}
	var ordered []zipEntry
	cursor := 0
	for index := 0; index < count; index++ {
		entry, next, err := readCentralEntry(central, cursor)
		if err != nil {
			return nil, err
		}
		cursor = next
		if err := validateEntry(entry, maxEntry); err != nil {
			return nil, err
		}
		if _, duplicate := manifest.entries[entry.name]; duplicate {
			return nil, fmt.Errorf("duplicate ZIP entry: %s", entry.name)
		}
		manifest.totalUncompressedBytes += entry.size
		if manifest.totalUncompressedBytes > maxTotal {
			return nil, fmt.Errorf("ZIP total uncompressed size exceeds the %d byte limit", maxTotal)
		}
		entry.dataStart, err = localDataStart(file, entry, centralStart)
		if err != nil {
			return nil, err
		}
		manifest.entries[entry.name] = entry
		ordered = append(ordered, entry)
		if required[entry.name] {
			if entry.isDir() || (entry.fileType() != 0 && entry.fileType() != typeRegular) {
				return nil, fmt.Errorf("required ZIP entry is not a regular file: %s", entry.name)
			}
			manifest.required[entry.name] = requiredEntry{Mode: entry.mode() & 0o7777, Size: entry.size}
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].headerOffset < ordered[j].headerOffset })
	for index, entry := range ordered {
		next := centralStart
		if index+1 < len(ordered) {
			next = ordered[index+1].headerOffset
		}
		if entry.dataStart > next || entry.dataStart+entry.compressedSize > next {
			return nil, fmt.Errorf("compressed data overlaps another local header or central directory for %s", entry.name)
		}
	}
	manifest.entryCount = count
	return manifest, nil
}

func readEndRecord(file *os.File, size int64) (start, length int64, count int, err error) {
	window := int64(22 + 0xffff)
	if window > size {
		window = size
	}
	tail := make([]byte, window)
	if _, err := file.ReadAt(tail, size-window); err != nil {
		return 0, 0, 0, fmt.Errorf("cannot read the ZIP end record: %v", err)
	}
	position := -1
	for index := len(tail) - 22; index >= 0; index-- {
		if binary.LittleEndian.Uint32(tail[index:]) == endRecordSignature {
			position = index
			break
		}
	}
	if position < 0 {
		return 0, 0, 0, errors.New("ZIP end of central directory record not found")
	}
	record := tail[position:]
	entries := binary.LittleEndian.Uint16(record[10:])
	length = int64(binary.LittleEndian.Uint32(record[12:]))
	start = int64(binary.LittleEndian.Uint32(record[16:]))
	if entries == 0xffff || length == 0xffffffff || start == 0xffffffff {
		return 0, 0, 0, errors.New("ZIP64 archives are not supported")
	}
	if binary.LittleEndian.Uint16(record[4:]) != 0 || binary.LittleEndian.Uint16(record[6:]) != 0 || binary.LittleEndian.Uint16(record[8:]) != entries {
		return 0, 0, 0, errors.New("multi-disk ZIP archives are not supported")
	}
	endOffset := size - window + int64(position)
	if start+length != endOffset {
		return 0, 0, 0, errors.New("ZIP central directory does not end at the end record")
	}
	return start, length, int(entries), nil
}

func readCentralEntry(central []byte, cursor int) (zipEntry, int, error) {
	if cursor+46 > len(central) || binary.LittleEndian.Uint32(central[cursor:]) != centralHeaderSignature {
		return zipEntry{}, 0, errors.New("truncated ZIP central directory")
	}
	header := central[cursor:]
	nameLength := int(binary.LittleEndian.Uint16(header[28:]))
	extraLength := int(binary.LittleEndian.Uint16(header[30:]))
	commentLength := int(binary.LittleEndian.Uint16(header[32:]))
	end := cursor + 46 + nameLength + extraLength + commentLength
	if end > len(central) {
		return zipEntry{}, 0, errors.New("truncated ZIP central directory")
	}
	entry := zipEntry{
		flags:          binary.LittleEndian.Uint16(header[8:]),
		method:         binary.LittleEndian.Uint16(header[10:]),
		crc:            binary.LittleEndian.Uint32(header[16:]),
		compressedSize: int64(binary.LittleEndian.Uint32(header[20:])),
		size:           int64(binary.LittleEndian.Uint32(header[24:])),
		externalAttrs:  binary.LittleEndian.Uint32(header[38:]),
		headerOffset:   int64(binary.LittleEndian.Uint32(header[42:])),
	}
	raw := header[46 : 46+nameLength]
	if entry.compressedSize == 0xffffffff || entry.size == 0xffffffff || entry.headerOffset == 0xffffffff {
		return zipEntry{}, 0, fmt.Errorf("ZIP64 entries are not supported: %q", raw)
	}
	// Names that a decoder could read differently (NUL truncation, legacy
	// code pages) are ambiguous between this check and the real installer.
	if bytes.IndexByte(raw, 0) >= 0 || (entry.flags&0x800 == 0 && !isASCII(raw)) || !utf8.Valid(raw) {
		return zipEntry{}, 0, fmt.Errorf("ambiguous ZIP entry filename: %q", raw)
	}
	entry.name = string(raw)
	return entry, end, nil
}

func isASCII(value []byte) bool {
	for _, b := range value {
		if b >= 0x80 {
			return false
		}
	}
	return true
}

func validateEntry(entry zipEntry, maxEntry int64) error {
	name := entry.name
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || (len(name) > 1 && name[1] == ':') {
		return fmt.Errorf("unsafe ZIP entry path: %q", name)
	}
	for _, char := range name {
		if char < 32 {
			return fmt.Errorf("unsafe ZIP entry path: %q", name)
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return fmt.Errorf("unsafe ZIP entry path: %q", name)
		}
	}
	if entry.flags&1 != 0 {
		return fmt.Errorf("encrypted ZIP entry is not supported: %s", name)
	}
	switch entry.fileType() {
	case 0, typeRegular, typeDirectory:
	default:
		return fmt.Errorf("unsupported ZIP entry type: %s", name)
	}
	if entry.isDir() && entry.fileType() != 0 && entry.fileType() != typeDirectory {
		return fmt.Errorf("ZIP directory has a non-directory type: %s", name)
	}
	if !entry.isDir() && entry.fileType() == typeDirectory {
		return fmt.Errorf("ZIP file has a directory type: %s", name)
	}
	if entry.size > maxEntry {
		return fmt.Errorf("ZIP entry exceeds the %d byte limit: %s", maxEntry, name)
	}
	return nil
}

func localDataStart(file *os.File, entry zipEntry, centralStart int64) (int64, error) {
	offset := entry.headerOffset
	if offset+30 > centralStart {
		return 0, fmt.Errorf("cannot verify local filename bounds for %s", entry.name)
	}
	header := make([]byte, 30)
	if _, err := file.ReadAt(header, offset); err != nil || binary.LittleEndian.Uint32(header) != localHeaderSignature {
		return 0, fmt.Errorf("cannot verify local filename header for %s", entry.name)
	}
	localFlags := binary.LittleEndian.Uint16(header[6:])
	if localFlags&0x8 != 0 || entry.flags&0x8 != 0 {
		return 0, fmt.Errorf("data descriptor entries cannot be bounded for %s", entry.name)
	}
	localCompressed := int64(binary.LittleEndian.Uint32(header[18:]))
	localSize := int64(binary.LittleEndian.Uint32(header[22:]))
	if localCompressed == 0xffffffff || localSize == 0xffffffff {
		return 0, fmt.Errorf("ZIP64 local size fields cannot be bounded for %s", entry.name)
	}
	if localCompressed != entry.compressedSize || localSize != entry.size {
		return 0, fmt.Errorf("local and central size fields differ for %s", entry.name)
	}
	nameLength := int64(binary.LittleEndian.Uint16(header[26:]))
	extraLength := int64(binary.LittleEndian.Uint16(header[28:]))
	if nameLength != int64(len(entry.name)) {
		return 0, fmt.Errorf("local filename length differs from central directory for %s", entry.name)
	}
	if offset+30+nameLength+extraLength > centralStart {
		return 0, fmt.Errorf("truncated local header for %s", entry.name)
	}
	localName := make([]byte, nameLength)
	if _, err := file.ReadAt(localName, offset+30); err != nil || string(localName) != entry.name {
		return 0, fmt.Errorf("local filename differs from central directory for %s", entry.name)
	}
	return offset + 30 + nameLength + extraLength, nil
}

// readEntry returns the content of one validated entry, refusing to produce
// more than limit bytes and verifying the CRC.
func readEntry(file *os.File, entry zipEntry, limit int64) ([]byte, error) {
	if entry.size > limit {
		return nil, fmt.Errorf("ZIP entry exceeds the %d byte limit: %s", limit, entry.name)
	}
	section := io.NewSectionReader(file, entry.dataStart, entry.compressedSize)
	var reader io.Reader
	switch entry.method {
	case 0:
		reader = section
	case 8:
		inflater := flate.NewReader(section)
		defer inflater.Close()
		reader = inflater
	default:
		return nil, fmt.Errorf("unsupported ZIP compression method %d: %s", entry.method, entry.name)
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %v", entry.name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("ZIP entry output exceeds the %d byte limit: %s", limit, entry.name)
	}
	if int64(len(data)) != entry.size || crc32.ChecksumIEEE(data) != entry.crc {
		return nil, fmt.Errorf("ZIP entry content does not match its declared size and CRC: %s", entry.name)
	}
	return data, nil
}
