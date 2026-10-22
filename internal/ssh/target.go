package ssh

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// Target describes the remote platform and the paths visible through its SSH
// shell. Identity is the target-side user identity used by supervisor state.
type Target struct {
	GOOS     string
	GOARCH   string
	GOARM    string
	DataRoot string
	Identity string
}

// SupportedTargets returns the standalone helper platforms in deterministic
// packaging order. It does not probe or claim access to a remote host.
func SupportedTargets() []Target {
	return []Target{
		{GOOS: "linux", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "arm64"},
		{GOOS: "linux", GOARCH: "arm", GOARM: "7"},
		{GOOS: "linux", GOARCH: "riscv64"},
		{GOOS: "linux", GOARCH: "loong64"},
		{GOOS: "darwin", GOARCH: "amd64"},
		{GOOS: "darwin", GOARCH: "arm64"},
		{GOOS: "windows", GOARCH: "amd64"},
		{GOOS: "windows", GOARCH: "arm64"},
	}
}

func (t Target) ID() string {
	architecture := t.GOARCH
	if t.GOOS == "linux" && t.GOARCH == "arm" && t.GOARM == "7" {
		architecture = "armv7"
	}
	return t.GOOS + "-" + architecture
}

func (t Target) validatePlatform() error {
	for _, supported := range SupportedTargets() {
		if t.GOOS == supported.GOOS && t.GOARCH == supported.GOARCH && t.GOARM == supported.GOARM {
			return nil
		}
	}
	return fmt.Errorf("%w: unsupported helper target %s/%s GOARM=%s", base.ErrUnsupported, t.GOOS, t.GOARCH, t.GOARM)
}

// TargetPaths contains remote, not local-host, paths.
type TargetPaths struct {
	Root     string
	BinDir   string
	StateDir string
	target   Target
}

func (t Target) Paths() (TargetPaths, error) {
	if err := t.validatePlatform(); err != nil {
		return TargetPaths{}, err
	}
	if t.DataRoot == "" || strings.ContainsRune(t.DataRoot, '\x00') {
		return TargetPaths{}, fmt.Errorf("target data root is required")
	}
	if t.GOOS == "windows" {
		if !windowsAbsolutePath(t.DataRoot) {
			return TargetPaths{}, fmt.Errorf("Windows target data root %q is not absolute", t.DataRoot)
		}
	} else if !path.IsAbs(t.DataRoot) {
		return TargetPaths{}, fmt.Errorf("POSIX target data root %q is not absolute", t.DataRoot)
	}
	root := remoteTargetJoin(t, t.DataRoot, "nexterm")
	return TargetPaths{
		Root:     root,
		BinDir:   remoteTargetJoin(t, root, "bin"),
		StateDir: remoteTargetJoin(t, root, "daemon"),
		target:   t,
	}, nil
}

func (p TargetPaths) Helper(digest string) (string, error) {
	if err := validateContentDigest(digest); err != nil {
		return "", err
	}
	name := "nexterm-helper-" + strings.ToLower(digest)
	if p.target.GOOS == "windows" {
		name += ".exe"
	}
	return remoteTargetJoin(p.target, p.BinDir, name), nil
}

func (p TargetPaths) installLock(digest string) (string, error) {
	if err := validateContentDigest(digest); err != nil {
		return "", err
	}
	return remoteTargetJoin(p.target, p.BinDir, ".nexterm-"+strings.ToLower(digest)+".install"), nil
}

func (p TargetPaths) staging(digest, nonce string) (string, error) {
	helper, err := p.Helper(digest)
	if err != nil {
		return "", err
	}
	if len(nonce) != 8 {
		return "", fmt.Errorf("staging nonce must be four bytes")
	}
	if _, err := hex.DecodeString(nonce); err != nil {
		return "", fmt.Errorf("staging nonce is malformed: %w", err)
	}
	directory := remoteTargetDir(p.target, helper)
	name := remoteTargetBase(p.target, helper)
	suffix := ".partial"
	if p.target.GOOS == "windows" {
		name = strings.TrimSuffix(name, ".exe")
		suffix = ".partial.exe"
	}
	return remoteTargetJoin(p.target, directory, "."+name+"."+nonce+suffix), nil
}

func validateContentDigest(digest string) error {
	if len(digest) != sha256HexLength {
		return fmt.Errorf("SHA256 digest must contain %d hexadecimal characters", sha256HexLength)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("SHA256 digest is malformed: %w", err)
	}
	return nil
}

func remoteTargetJoin(target Target, parent, child string) string {
	if target.GOOS != "windows" {
		return path.Join(parent, child)
	}
	parent = strings.ReplaceAll(parent, "/", `\`)
	child = strings.ReplaceAll(child, "/", `\`)
	if strings.HasSuffix(parent, `\`) {
		return parent + child
	}
	return parent + `\` + child
}

func remoteTargetDir(target Target, value string) string {
	if target.GOOS != "windows" {
		return path.Dir(value)
	}
	value = strings.TrimRight(strings.ReplaceAll(value, "/", `\`), `\`)
	if index := strings.LastIndex(value, `\`); index >= 0 {
		if index == 2 && len(value) >= 3 && value[1] == ':' {
			return value[:3]
		}
		return value[:index]
	}
	return value
}

func remoteTargetBase(target Target, value string) string {
	if target.GOOS != "windows" {
		return path.Base(value)
	}
	value = strings.TrimRight(strings.ReplaceAll(value, "/", `\`), `\`)
	if index := strings.LastIndex(value, `\`); index >= 0 {
		return value[index+1:]
	}
	return value
}

func windowsAbsolutePath(value string) bool {
	value = strings.ReplaceAll(value, "/", `\`)
	if strings.HasPrefix(value, `\\`) {
		return len(value) > 2
	}
	return len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && value[2] == '\\'
}

const posixTargetDiscovery = `os=$(uname -s) || exit $?
machine=$(uname -m) || exit $?
identity=$(id -u) || exit $?
case "$os" in
Darwin) root=${XDG_DATA_HOME:-$HOME/Library/Application Support} ;;
*) root=${XDG_DATA_HOME:-$HOME/.local/share} ;;
esac
printf '%s\n' "$root" "$os" "$machine" "$identity"`

const windowsTargetDiscovery = `$ErrorActionPreference='Stop'
[Console]::OutputEncoding=[Text.Encoding]::UTF8
$root=[Environment]::GetFolderPath('LocalApplicationData')
$architecture=[System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
$identity=[System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
ConvertTo-Json -InputObject ([PSCustomObject]@{data_root=$root;architecture=$architecture;identity=$identity}) -Compress`

// DiscoverTarget probes a target through its SSH exec shell. POSIX discovery
// uses uname; a failed POSIX probe is followed by an explicit Windows OpenSSH
// PowerShell probe, with both failures preserved.
func DiscoverTarget(ctx context.Context, runner Runner) (Target, error) {
	if runner == nil {
		return Target{}, fmt.Errorf("target discovery requires a runner")
	}
	stdout, exitCode, err := runner.Run(ctx, posixTargetDiscovery, probeTimeout)
	if err != nil {
		return Target{}, fmt.Errorf("POSIX target discovery: %w", err)
	}
	if exitCode == 0 {
		return parsePOSIXTarget(stdout)
	}
	windows, windowsErr := discoverWindowsTarget(ctx, runner)
	if windowsErr == nil {
		return windows, nil
	}
	return Target{}, fmt.Errorf("POSIX target discovery exited with %d; Windows target discovery: %w", exitCode, windowsErr)
}

func parsePOSIXTarget(stdout string) (Target, error) {
	lines := nonEmptyLines(stdout)
	if len(lines) != 4 {
		return Target{}, fmt.Errorf("unexpected POSIX target discovery output %q", stdout)
	}
	goos := strings.ToLower(lines[1])
	if goos != "linux" && goos != "darwin" {
		return Target{}, fmt.Errorf("%w: unsupported target OS %q", base.ErrUnsupported, lines[1])
	}
	goarch, goarm, err := targetMachineArchitecture(lines[2])
	if err != nil {
		return Target{}, err
	}
	target := Target{GOOS: goos, GOARCH: goarch, GOARM: goarm, DataRoot: lines[0], Identity: lines[3]}
	if _, err := strconv.Atoi(target.Identity); err != nil {
		return Target{}, fmt.Errorf("target discovery returned non-numeric uid %q", target.Identity)
	}
	if target.Identity == "" {
		return Target{}, fmt.Errorf("target identity is empty")
	}
	if err := target.validatePlatform(); err != nil {
		return Target{}, err
	}
	if _, err := target.Paths(); err != nil {
		return Target{}, err
	}
	return target, nil
}

func targetMachineArchitecture(machine string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(machine)) {
	case "x86_64", "amd64":
		return "amd64", "", nil
	case "aarch64", "arm64":
		return "arm64", "", nil
	case "armv7", "armv7l", "armv7hl", "armhf":
		return "arm", "7", nil
	case "riscv64":
		return "riscv64", "", nil
	case "loong64", "loongarch64":
		return "loong64", "", nil
	default:
		return "", "", fmt.Errorf("%w: unsupported target architecture %q", base.ErrUnsupported, machine)
	}
}

type windowsTargetDocument struct {
	DataRoot     string `json:"data_root"`
	Architecture string `json:"architecture"`
	Identity     string `json:"identity"`
}

func discoverWindowsTarget(ctx context.Context, runner Runner) (Target, error) {
	stdout, exitCode, err := runner.Run(ctx, powerShellCommand("powershell.exe", windowsTargetDiscovery), probeTimeout)
	if err != nil {
		return Target{}, err
	}
	if exitCode != 0 {
		return Target{}, fmt.Errorf("PowerShell discovery exited with %d", exitCode)
	}
	var document windowsTargetDocument
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &document); err != nil {
		return Target{}, fmt.Errorf("decode Windows discovery output: %w", err)
	}
	var goarch string
	switch strings.ToLower(document.Architecture) {
	case "x64":
		goarch = "amd64"
	case "arm64":
		goarch = "arm64"
	default:
		return Target{}, fmt.Errorf("%w: unsupported Windows architecture %q", base.ErrUnsupported, document.Architecture)
	}
	target := Target{GOOS: "windows", GOARCH: goarch, DataRoot: document.DataRoot, Identity: document.Identity}
	if target.Identity == "" {
		return Target{}, fmt.Errorf("Windows target identity is empty")
	}
	if _, err := target.Paths(); err != nil {
		return Target{}, err
	}
	return target, nil
}

// LaunchCommand returns a detached helper launch command for the target. macOS
// uses nohup rather than the unavailable setsid; Windows uses Start-Process.
func (t Target) LaunchCommand(binary, stateDir string) (string, error) {
	if err := t.validatePlatform(); err != nil {
		return "", err
	}
	if binary == "" || stateDir == "" || strings.ContainsRune(binary, '\x00') || strings.ContainsRune(stateDir, '\x00') {
		return "", fmt.Errorf("helper binary and state directory are required")
	}
	if t.GOOS != "windows" {
		launcher := "setsid"
		chmodCommand := "chmod 700 -- "
		if t.GOOS == "darwin" {
			launcher = "nohup"
			chmodCommand = "chmod 700 "
		}
		logPath := remoteTargetJoin(t, stateDir, "helper.log")
		return "mkdir -p -- " + quoteShell(stateDir) + " && " + chmodCommand + quoteShell(stateDir) +
			" && { " + launcher + " " + quoteShell(binary) + " " + supervisor.HelperCommand + " --state-dir " + quoteShell(stateDir) +
			" >>" + quoteShell(logPath) + " 2>&1 </dev/null & }", nil
	}
	stdout := remoteTargetJoin(t, stateDir, "helper.log")
	stderr := remoteTargetJoin(t, stateDir, "helper.error.log")
	arguments := supervisor.HelperCommand + " --state-dir " + quoteWindowsArgument(stateDir)
	script := "$ErrorActionPreference='Stop'; " +
		"New-Item -ItemType Directory -Force -Path " + quotePowerShell(stateDir) + " | Out-Null; " +
		"$process=Start-Process -FilePath " + quotePowerShell(binary) + " -ArgumentList " + quotePowerShell(arguments) +
		" -WindowStyle Hidden -RedirectStandardOutput " + quotePowerShell(stdout) + " -RedirectStandardError " + quotePowerShell(stderr) + " -PassThru; " +
		"[Console]::Out.Write($process.Id)"
	return powerShellCommand("powershell.exe", script), nil
}

func (t Target) probeCommand(binary string) (string, error) {
	if err := t.validatePlatform(); err != nil {
		return "", err
	}
	if t.GOOS == "windows" {
		return powerShellCommand("powershell.exe", "& "+quotePowerShell(binary)+" "+supervisor.HelperCommand+" --probe"), nil
	}
	return quoteShell(binary) + " " + supervisor.HelperCommand + " --probe", nil
}
