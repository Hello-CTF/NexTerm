package ids

import (
	"crypto/rand"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// New returns a 26-character ULID with a millisecond timestamp and 80 random bits.
func New() string {
	var raw [16]byte
	now := uint64(time.Now().UnixMilli())
	raw[0] = byte(now >> 40)
	raw[1] = byte(now >> 32)
	raw[2] = byte(now >> 24)
	raw[3] = byte(now >> 16)
	raw[4] = byte(now >> 8)
	raw[5] = byte(now)
	_, _ = rand.Read(raw[6:])

	var encoded [26]byte
	for i := range encoded {
		var value byte
		for bit := 0; bit < 5; bit++ {
			value <<= 1
			sourceBit := i*5 + bit - 2
			if sourceBit >= 0 {
				value |= (raw[sourceBit/8] >> (7 - sourceBit%8)) & 1
			}
		}
		encoded[i] = crockford[value]
	}
	return string(encoded[:])
}

func NowMS() int64 {
	return time.Now().UnixMilli()
}

func Valid(id string) bool {
	if len(id) != 26 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}
