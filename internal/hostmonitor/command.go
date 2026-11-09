package hostmonitor

import "strings"

const sentinelPrefix = "@@nexterm-monitor "

// Section names written by SampleCommand, in output order.
const (
	sectionOS      = "os"
	sectionStat    = "stat"
	sectionMeminfo = "meminfo"
	sectionLoadavg = "loadavg"
	sectionNetDev  = "netdev"
	sectionUptime  = "uptime"
	sectionDF      = "df"
	sectionEnd     = "end"
)

// sampleScript reads every metric of one sample in a single round trip. Only the cpu lines of /proc/stat are
// read because the rest of the file (interrupt counters) grows with the core count. df -l keeps network mounts,
// which can hang, out of the listing; a df without -l (busybox) falls back to the root file system alone.
const sampleScript = `LC_ALL=C; export LC_ALL
s() { printf '` + sentinelPrefix + `%s\n' "$*"; }
os=$(uname -s 2>/dev/null)
s ` + sectionOS + ` "$os"
if [ "$os" = Linux ]; then
s ` + sectionStat + `; grep '^cpu' /proc/stat 2>/dev/null
s ` + sectionMeminfo + `; cat /proc/meminfo 2>/dev/null
s ` + sectionLoadavg + `; cat /proc/loadavg 2>/dev/null
s ` + sectionNetDev + `; cat /proc/net/dev 2>/dev/null
s ` + sectionUptime + `; cat /proc/uptime 2>/dev/null
s ` + sectionDF + `; df -P -l 2>/dev/null || df -P / 2>/dev/null
fi
s ` + sectionEnd

// SampleCommand returns the command that collects one sample. The script runs under sh so that a login shell
// such as fish or cmd.exe never has to understand it; a host without sh prints no sentinel and is reported as
// unsupported.
func SampleCommand() string {
	return "sh -c " + shellQuote(sampleScript)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
