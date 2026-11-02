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

// ReleaseChannel selects which releases the updater offers. Stable is the default everywhere.
type ReleaseChannel string

const (
	ChannelStable ReleaseChannel = "stable"
	ChannelRC     ReleaseChannel = "rc"
)

// ReleaseChannelSettingKey is the local-only Setting row holding the channel. It is deliberately outside every
// synced key family (prefs.user.*, ai and sync metadata), so the choice never leaves this machine.
const ReleaseChannelSettingKey = "update.releaseChannel"

// ParseReleaseChannel accepts exactly the two known channel names.
func ParseReleaseChannel(value string) (ReleaseChannel, bool) {
	switch ReleaseChannel(value) {
	case ChannelStable:
		return ChannelStable, true
	case ChannelRC:
		return ChannelRC, true
	}
	return "", false
}

// NormalizeReleaseChannel maps a missing or invalid stored value to stable.
func NormalizeReleaseChannel(value string) ReleaseChannel {
	channel, _ := ParseReleaseChannel(value)
	if channel == "" {
		return ChannelStable
	}
	return channel
}

// isRCPrerelease reports whether a prerelease suffix is exactly rc.N; any other prerelease form is ignored
// on every channel.
func isRCPrerelease(pre string) bool {
	number, ok := strings.CutPrefix(pre, "rc.")
	if !ok || number == "" || (len(number) > 1 && number[0] == '0') {
		return false
	}
	for _, r := range number {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// releaseVisibleOn reports whether a published release may be offered on the channel: stable releases are
// visible everywhere, rc.N prereleases only on the RC channel, every other prerelease form nowhere.
func releaseVisibleOn(release Release, channel ReleaseChannel) bool {
	if release.Draft {
		return false
	}
	parsed, ok := parseVersion(release.version())
	if !ok {
		return false
	}
	if !parsed.hasPre {
		return !release.Prerelease
	}
	return release.Prerelease && channel == ChannelRC && isRCPrerelease(parsed.pre)
}

// runningPrerelease reports whether the running build is a prerelease build (for example 1.4.0-rc.1).
func runningPrerelease(current string) bool {
	parsed, ok := parseVersion(current)
	return ok && parsed.hasPre
}
