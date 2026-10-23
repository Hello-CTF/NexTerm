package main

import (
	"encoding/binary"
	"errors"
	"fmt"
)

type machOFormat struct {
	order binary.ByteOrder
	is64  bool
}

var thinMachOFormats = map[uint32]machOFormat{
	0xfeedface: {binary.BigEndian, false},
	0xcefaedfe: {binary.LittleEndian, false},
	0xfeedfacf: {binary.BigEndian, true},
	0xcffaedfe: {binary.LittleEndian, true},
}

var fatMachOFormats = map[uint32]machOFormat{
	0xcafebabe: {binary.BigEndian, false},
	0xbebafeca: {binary.LittleEndian, false},
	0xcafebabf: {binary.BigEndian, true},
	0xbfbafeca: {binary.LittleEndian, true},
}

const loadCommandBuildVersion = 0x32

func readU32(data []byte, offset uint64, order binary.ByteOrder) (uint64, error) {
	if offset+4 > uint64(len(data)) {
		return 0, errors.New("truncated Mach-O structure")
	}
	return uint64(order.Uint32(data[offset:])), nil
}

func readU64(data []byte, offset uint64, order binary.ByteOrder) (uint64, error) {
	if offset+8 > uint64(len(data)) {
		return 0, errors.New("truncated Mach-O structure")
	}
	return order.Uint64(data[offset:]), nil
}

// thinMachOPlatform returns the single LC_BUILD_VERSION platform of one slice.
func thinMachOPlatform(data []byte, offset, size uint64) (uint64, error) {
	if offset+4 > uint64(len(data)) {
		return 0, errors.New("truncated Mach-O header")
	}
	format, ok := thinMachOFormats[binary.BigEndian.Uint32(data[offset:])]
	if !ok {
		return 0, errors.New("unknown Mach-O magic")
	}
	headerSize := uint64(28)
	if format.is64 {
		headerSize = 32
	}
	if size < headerSize || offset+headerSize > uint64(len(data)) {
		return 0, errors.New("truncated Mach-O header")
	}
	count, _ := readU32(data, offset+16, format.order)
	commandsSize, _ := readU32(data, offset+20, format.order)
	start := offset + headerSize
	end := start + commandsSize
	if count > commandsSize/8 || end > offset+size || end > uint64(len(data)) {
		return 0, errors.New("invalid Mach-O load command region")
	}
	var platform uint64
	found := false
	cursor := start
	for index := uint64(0); index < count; index++ {
		command, err := readU32(data, cursor, format.order)
		if err != nil {
			return 0, err
		}
		commandSize, err := readU32(data, cursor+4, format.order)
		if err != nil {
			return 0, err
		}
		if commandSize < 8 || cursor+commandSize > end {
			return 0, errors.New("invalid Mach-O load command size")
		}
		if command == loadCommandBuildVersion {
			if commandSize < 24 {
				return 0, errors.New("truncated LC_BUILD_VERSION command")
			}
			candidate, err := readU32(data, cursor+8, format.order)
			if err != nil {
				return 0, err
			}
			if found && candidate != platform {
				return 0, errors.New("conflicting LC_BUILD_VERSION platforms")
			}
			platform, found = candidate, true
		}
		cursor += commandSize
	}
	if !found {
		return 0, errors.New("Mach-O has no LC_BUILD_VERSION command")
	}
	return platform, nil
}

// machOBuildPlatforms returns the build platform of every slice in a thin or
// fat Mach-O executable.
func machOBuildPlatforms(data []byte) ([]uint64, error) {
	if len(data) < 4 {
		return nil, errors.New("truncated Mach-O magic")
	}
	fat, ok := fatMachOFormats[binary.BigEndian.Uint32(data)]
	if !ok {
		platform, err := thinMachOPlatform(data, 0, uint64(len(data)))
		if err != nil {
			return nil, err
		}
		return []uint64{platform}, nil
	}
	count, err := readU32(data, 4, fat.order)
	if err != nil {
		return nil, err
	}
	entrySize := uint64(20)
	if fat.is64 {
		entrySize = 32
	}
	tableEnd := 8 + count*entrySize
	if count < 1 || tableEnd > uint64(len(data)) {
		return nil, errors.New("invalid fat Mach-O architecture table")
	}
	platforms := make([]uint64, 0, count)
	for index := uint64(0); index < count; index++ {
		architecture := 8 + index*entrySize
		var offset, size uint64
		if fat.is64 {
			offset, _ = readU64(data, architecture+8, fat.order)
			size, _ = readU64(data, architecture+16, fat.order)
		} else {
			offset, _ = readU32(data, architecture+8, fat.order)
			size, _ = readU32(data, architecture+12, fat.order)
		}
		if offset < tableEnd || size < 4 || offset > uint64(len(data)) || size > uint64(len(data))-offset {
			return nil, errors.New("invalid fat Mach-O slice bounds")
		}
		platform, err := thinMachOPlatform(data, offset, size)
		if err != nil {
			return nil, fmt.Errorf("invalid Mach-O slice %d: %v", index, err)
		}
		platforms = append(platforms, platform)
	}
	return platforms, nil
}
