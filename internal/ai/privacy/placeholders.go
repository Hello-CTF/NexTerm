// Adapted from AstrLink (Apache-2.0), commit 24ddf0d823785fe9b3ed7ee23b3a0fc517313e93.
// See third_party/ASTRLINK_NOTICE.md and third_party/ASTRLINK_LICENSE.

package privacy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

const placeholderTokenHexLength = 16
const placeholderDerivationTrials = 64

type Kind string

const (
	KindEmail        Kind = "email"
	KindPhone        Kind = "phone"
	KindAccount      Kind = "account"
	KindPaymentCard  Kind = "payment_card"
	KindIPAddress    Kind = "ip_address"
	KindURL          Kind = "url"
	KindCommonSecret Kind = "common_secret"
	KindAddress      Kind = "private_address"
	KindDate         Kind = "private_date"
	KindPerson       Kind = "private_person"
)

type Redaction struct {
	Placeholder string
	Kind        Kind
	Value       string
}

type placeholderIdentity struct {
	kind  Kind
	value string
}

// placeholderAllocator retains AstrLink's keyed, kind-aware derivation and
// bounded collision retries. The key is already isolated to a NexTerm owner
// and conversation. Only token placeholders are offered, so no finite IPv4
// namespace or persistent natural-stand-in ledger is required.
type placeholderAllocator struct {
	key      [32]byte
	used     map[string]placeholderIdentity
	assigned map[placeholderIdentity]string
}

func newPlaceholderAllocator(key [32]byte) *placeholderAllocator {
	return &placeholderAllocator{key: key, used: make(map[string]placeholderIdentity), assigned: make(map[placeholderIdentity]string)}
}

func (allocator *placeholderAllocator) allocate(kind Kind, value string) (string, error) {
	identity := placeholderIdentity{kind: kind, value: value}
	if existing, ok := allocator.assigned[identity]; ok {
		return existing, nil
	}
	for trial := range placeholderDerivationTrials {
		suffix := allocator.derive(kind, value, trial)
		candidate := withPlaceholderSuffix(replacementFor(kind), suffix)
		if _, used := allocator.used[candidate]; used {
			continue
		}
		allocator.used[candidate] = identity
		allocator.assigned[identity] = candidate
		return candidate, nil
	}
	return "", errors.New("privacy placeholder namespace exhausted")
}

func (allocator *placeholderAllocator) derive(kind Kind, value string, trial int) string {
	mac := hmac.New(sha256.New, allocator.key[:])
	mac.Write([]byte(kind))
	mac.Write([]byte{0})
	mac.Write([]byte(value))
	mac.Write([]byte{0})
	mac.Write([]byte{byte(trial >> 8), byte(trial)})
	return hex.EncodeToString(mac.Sum(nil))[:placeholderTokenHexLength]
}

func withPlaceholderSuffix(base, suffix string) string {
	if strings.HasPrefix(base, "<") && strings.HasSuffix(base, ">") {
		return base[:len(base)-1] + "_" + suffix + ">"
	}
	return base + "_" + suffix
}

func replacementFor(kind Kind) string {
	switch kind {
	case KindEmail:
		return "<PRIVATE_EMAIL>"
	case KindPhone:
		return "<PRIVATE_PHONE>"
	case KindAccount:
		return "<PRIVATE_ACCOUNT_NUMBER>"
	case KindPaymentCard:
		return "<PRIVATE_PAYMENT_CARD>"
	case KindIPAddress:
		return "<PRIVATE_IP_ADDRESS>"
	case KindURL:
		return "<PRIVATE_URL>"
	case KindCommonSecret:
		return "<SECRET>"
	case KindAddress:
		return "<PRIVATE_ADDRESS>"
	case KindDate:
		return "<PRIVATE_DATE>"
	case KindPerson:
		return "<PRIVATE_PERSON>"
	default:
		return "<PRIVATE>"
	}
}

// isTokenPlaceholderSpan preserves a marker quoted in reasoning on later turns.
// Compared with upstream, only the allocator's exact suffixed token formats
// qualify; arbitrary <SECRET ...> input must still be inspected.
func isTokenPlaceholderSpan(text string, start, end int) bool {
	if start < 0 || end > len(text) || start >= end {
		return false
	}
	value := text[start:end]
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	start += strings.Index(value, trimmed)
	end = start + len(trimmed)
	opening := start
	if text[opening] != '<' {
		opening = strings.LastIndexAny(text[:start], "<>")
	}
	if opening < 0 || text[opening] != '<' {
		return false
	}
	closing := strings.IndexAny(text[opening+1:], "<>")
	if closing < 0 {
		return false
	}
	closing += opening + 1
	if text[closing] != '>' || end > closing+1 {
		return false
	}
	body, ok := markerBody(text[opening : closing+1])
	if !ok {
		return false
	}
	base := body[:len(body)-placeholderTokenHexLength-1]
	for _, kind := range []Kind{"", KindEmail, KindPhone, KindAccount, KindPaymentCard, KindIPAddress, KindURL, KindCommonSecret, KindAddress, KindDate, KindPerson} {
		marker := replacementFor(kind)
		if base == marker[1:len(marker)-1] {
			return true
		}
	}
	return false
}

func isASCIIDigit(character byte) bool { return character >= '0' && character <= '9' }

const maxJSONDepth = 128

var ErrUnsafeInput = errors.New("privacy requires unambiguous UTF-8 JSON")

func isJSONNumberByte(character byte) bool {
	return isASCIIDigit(character) || character == '-' || character == '+' || character == '.' || character == 'e' || character == 'E'
}
func isJSONWhitespace(character byte) bool {
	return character == ' ' || character == '\t' || character == '\n' || character == '\r'
}
