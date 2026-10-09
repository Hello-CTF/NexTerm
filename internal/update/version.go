package update

import (
	"strconv"
	"strings"
)

type parsedVersion struct {
	numbers [3]int
	pre     string
	hasPre  bool
}

func parseVersion(value string) (parsedVersion, bool) {
	var parsed parsedVersion
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if index := strings.IndexByte(value, '+'); index >= 0 {
		value = value[:index]
	}
	if index := strings.IndexByte(value, '-'); index >= 0 {
		parsed.pre = value[index+1:]
		parsed.hasPre = true
		value = value[:index]
	}
	parts := strings.Split(value, ".")
	if len(parts) > 3 {
		return parsedVersion{}, false
	}
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return parsedVersion{}, false
		}
		parsed.numbers[index] = number
	}
	return parsed, true
}

func compareVersions(left, right parsedVersion) int {
	for index := range left.numbers {
		if left.numbers[index] != right.numbers[index] {
			if left.numbers[index] < right.numbers[index] {
				return -1
			}
			return 1
		}
	}
	switch {
	case left.hasPre && !right.hasPre:
		return -1
	case !left.hasPre && right.hasPre:
		return 1
	case !left.hasPre && !right.hasPre:
		return 0
	}
	return comparePrerelease(left.pre, right.pre)
}

func comparePrerelease(left, right string) int {
	leftParts := strings.Split(left, ".")
	rightParts := strings.Split(right, ".")
	for index := 0; index < len(leftParts) && index < len(rightParts); index++ {
		leftNumber, leftErr := strconv.Atoi(leftParts[index])
		rightNumber, rightErr := strconv.Atoi(rightParts[index])
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNumber != rightNumber {
				if leftNumber < rightNumber {
					return -1
				}
				return 1
			}
		case leftErr == nil:
			return -1
		case rightErr == nil:
			return 1
		default:
			if leftParts[index] != rightParts[index] {
				if leftParts[index] < rightParts[index] {
					return -1
				}
				return 1
			}
		}
	}
	switch {
	case len(leftParts) < len(rightParts):
		return -1
	case len(leftParts) > len(rightParts):
		return 1
	}
	return 0
}

func isNewerVersion(candidate, current string) bool {
	candidateVersion, candidateOK := parseVersion(candidate)
	currentVersion, currentOK := parseVersion(current)
	if !candidateOK {
		return false
	}
	if !currentOK {
		return true
	}
	return compareVersions(candidateVersion, currentVersion) > 0
}

// acceptsPrerelease 按当前版本决定是否接受 prerelease: 当前版本带预发布后缀
// (如 0.2.2-rc.7) 或无法解析 (dev 构建) 时接受, 稳定版只接受稳定 release。
func acceptsPrerelease(current string) bool {
	parsed, ok := parseVersion(current)
	if !ok {
		return true
	}
	return parsed.hasPre
}
