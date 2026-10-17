package winrm

import (
	"encoding/binary"
	"fmt"
)

const (
	ntlmAvIDMsvAvEOL       = 0
	ntlmAvIDMsvAvFlags     = 6
	ntlmAvIDMsvAvTimestamp = 7
	ntlmAvFlagMICPresent   = 0x00000002
	ntlmNegotiateVersion   = 0x02000000
)

var ntlmVersionBlock = [8]byte{6, 1, 0xb1, 0x1d, 0, 0, 0, 0x0f}

var ntlmVarFieldOffsetPositions = [6]struct {
	lenPos, offsetPos int
}{
	{12, 16},
	{20, 24},
	{28, 32},
	{36, 40},
	{44, 48},
	{52, 56},
}

func ntlmChallengeTargetInfo(challengeToken []byte) (offset, length int, err error) {
	if len(challengeToken) < 48 {
		return 0, 0, fmt.Errorf("ntlmssp: challenge token too short to contain TargetInfo: %d bytes", len(challengeToken))
	}
	length = int(binary.LittleEndian.Uint16(challengeToken[40:42]))
	offset = int(binary.LittleEndian.Uint32(challengeToken[44:48]))
	if offset < 0 || length < 0 || offset+length > len(challengeToken) {
		return 0, 0, fmt.Errorf("ntlmssp: TargetInfo field (offset %d, length %d) extends beyond challenge token of %d bytes", offset, length, len(challengeToken))
	}
	return offset, length, nil
}

func ntlmWalkAVPairs(targetInfo []byte, visit func(avID uint16, value []byte, valueOffsetInBlob int) bool) error {
	pos := 0
	for {
		if pos+4 > len(targetInfo) {
			return fmt.Errorf("ntlmssp: AV_PAIR header extends beyond TargetInfo blob at offset %d (%d bytes total)", pos, len(targetInfo))
		}
		avID := binary.LittleEndian.Uint16(targetInfo[pos : pos+2])
		avLen := int(binary.LittleEndian.Uint16(targetInfo[pos+2 : pos+4]))
		valueOffset := pos + 4
		if valueOffset+avLen > len(targetInfo) {
			return fmt.Errorf("ntlmssp: AV_PAIR value extends beyond TargetInfo blob at offset %d (len %d, blob %d bytes)", valueOffset, avLen, len(targetInfo))
		}
		if visit(avID, targetInfo[valueOffset:valueOffset+avLen], valueOffset) {
			return nil
		}
		if avID == ntlmAvIDMsvAvEOL {
			return nil
		}
		pos = valueOffset + avLen
	}
}

func ntlmPatchChallengeForMIC(challengeToken []byte) (patched []byte, micRequired bool, err error) {
	offset, length, err := ntlmChallengeTargetInfo(challengeToken)
	if err != nil {
		return nil, false, err
	}
	if length == 0 {
		return challengeToken, false, nil
	}
	targetInfo := challengeToken[offset : offset+length]
	var hasTimestamp, hasFlags bool
	var flagsValueOff int
	eolOffset := length
	if err := ntlmWalkAVPairs(targetInfo, func(avID uint16, value []byte, valueOffsetInBlob int) bool {
		switch avID {
		case ntlmAvIDMsvAvTimestamp:
			hasTimestamp = true
		case ntlmAvIDMsvAvFlags:
			if len(value) != 4 {
				return false
			}
			hasFlags = true
			flagsValueOff = valueOffsetInBlob
		case ntlmAvIDMsvAvEOL:
			eolOffset = valueOffsetInBlob - 4
		}
		return false
	}); err != nil {
		return nil, false, err
	}
	if !hasTimestamp {
		return challengeToken, false, nil
	}
	var newTargetInfo []byte
	if hasFlags {
		newTargetInfo = append([]byte(nil), targetInfo...)
		flags := binary.LittleEndian.Uint32(newTargetInfo[flagsValueOff : flagsValueOff+4])
		flags |= ntlmAvFlagMICPresent
		binary.LittleEndian.PutUint32(newTargetInfo[flagsValueOff:flagsValueOff+4], flags)
	} else {
		newPair := make([]byte, 8)
		binary.LittleEndian.PutUint16(newPair[0:2], ntlmAvIDMsvAvFlags)
		binary.LittleEndian.PutUint16(newPair[2:4], 4)
		binary.LittleEndian.PutUint32(newPair[4:8], ntlmAvFlagMICPresent)
		newTargetInfo = make([]byte, 0, len(targetInfo)+8)
		newTargetInfo = append(newTargetInfo, targetInfo[:eolOffset]...)
		newTargetInfo = append(newTargetInfo, newPair...)
		newTargetInfo = append(newTargetInfo, targetInfo[eolOffset:]...)
	}
	if len(newTargetInfo) == length {
		patched = append([]byte(nil), challengeToken...)
		copy(patched[offset:offset+length], newTargetInfo)
		return patched, true, nil
	}
	patched = append([]byte(nil), challengeToken...)
	newOffset := len(patched)
	patched = append(patched, newTargetInfo...)
	binary.LittleEndian.PutUint16(patched[40:42], uint16(len(newTargetInfo)))
	binary.LittleEndian.PutUint16(patched[42:44], uint16(len(newTargetInfo)))
	binary.LittleEndian.PutUint32(patched[44:48], uint32(newOffset))
	return patched, true, nil
}

func ntlmInsertVersion(authenticateToken []byte, negotiatedFlags uint32) ([]byte, error) {
	if negotiatedFlags&ntlmNegotiateVersion == 0 {
		return authenticateToken, nil
	}
	if len(authenticateToken) < 64 {
		return nil, fmt.Errorf("ntlmssp: AUTHENTICATE token too short to contain a fixed header: %d bytes", len(authenticateToken))
	}
	withVersion := make([]byte, 0, len(authenticateToken)+8)
	withVersion = append(withVersion, authenticateToken[:64]...)
	withVersion = append(withVersion, ntlmVersionBlock[:]...)
	withVersion = append(withVersion, authenticateToken[64:]...)
	flags := binary.LittleEndian.Uint32(withVersion[60:64]) | ntlmNegotiateVersion
	binary.LittleEndian.PutUint32(withVersion[60:64], flags)
	for _, pos := range ntlmVarFieldOffsetPositions {
		bufferOffset := binary.LittleEndian.Uint32(withVersion[pos.offsetPos : pos.offsetPos+4])
		binary.LittleEndian.PutUint32(withVersion[pos.offsetPos:pos.offsetPos+4], bufferOffset+8)
	}
	return withVersion, nil
}

func ntlmAttachMIC(negotiateToken, challengeToken, authenticateToken, exportedSessionKey []byte) ([]byte, error) {
	if len(authenticateToken) < 64 {
		return nil, fmt.Errorf("ntlmssp: AUTHENTICATE token too short to contain a fixed header: %d bytes", len(authenticateToken))
	}
	headerEnd := 64
	if flags := binary.LittleEndian.Uint32(authenticateToken[60:64]); flags&ntlmNegotiateVersion != 0 {
		headerEnd = 72
	}
	if len(authenticateToken) < headerEnd {
		return nil, fmt.Errorf("ntlmssp: AUTHENTICATE token too short to contain its Version block: %d bytes", len(authenticateToken))
	}
	withMIC := make([]byte, 0, len(authenticateToken)+16)
	withMIC = append(withMIC, authenticateToken[:headerEnd]...)
	withMIC = append(withMIC, make([]byte, 16)...)
	withMIC = append(withMIC, authenticateToken[headerEnd:]...)
	for _, pos := range ntlmVarFieldOffsetPositions {
		bufferOffset := binary.LittleEndian.Uint32(withMIC[pos.offsetPos : pos.offsetPos+4])
		binary.LittleEndian.PutUint32(withMIC[pos.offsetPos:pos.offsetPos+4], bufferOffset+16)
	}
	mic := ntlmHmacMd5(exportedSessionKey, negotiateToken, challengeToken, withMIC)
	copy(withMIC[headerEnd:headerEnd+16], mic)
	return withMIC, nil
}
