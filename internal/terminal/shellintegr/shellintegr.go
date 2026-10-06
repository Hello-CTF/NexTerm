// Package shellintegr injects a cwd-reporting and command-boundary wrapper
// into interactive zsh, bash, and fish sessions, and parses the resulting
// OSC 7 cwd reports and OSC 133 command lifecycle sequences back out of the
// terminal byte stream.
//
// The wrapper only adds escape-sequence output on top of what the shell
// already writes; it never rewrites or suppresses shell output. Tracker and
// CommandTracker read the stream without consuming or mutating it, so
// recording and replay see exactly the bytes the shell produced.
package shellintegr

import (
	"fmt"
	"os"
	"path/filepath"
)

// Shell identifies a shell supported by this package.
type Shell string

const (
	ShellZsh  Shell = "zsh"
	ShellBash Shell = "bash"
	ShellFish Shell = "fish"
)

// Detect maps a shell binary path or name to a supported Shell. Shells
// without a wrapper return an error so callers can launch them unwrapped.
func Detect(path string) (Shell, error) {
	switch shell := Shell(filepath.Base(path)); shell {
	case ShellZsh, ShellBash, ShellFish:
		return shell, nil
	}
	return "", fmt.Errorf("shellintegr: unsupported shell %q", path)
}

// Launch describes an interactive shell process with cwd reporting enabled.
type Launch struct {
	Path string
	Args []string
	// Env lists environment entries the launch requires (zsh needs ZDOTDIR).
	// Replace any existing entry with the same key before appending: on Unix
	// the first occurrence of a duplicated key wins.
	Env []string
	// Cleanup removes the wrapper files. Call it once the process has exited;
	// extra calls are harmless.
	Cleanup func()
}

// Wrap builds the command for an interactive shell of the given kind with the
// cwd-reporting wrapper injected. An empty path defaults to the shell name.
// The caller appends Env to the process environment and execs Path with Args.
func Wrap(shell Shell, path string) (*Launch, error) {
	if path == "" {
		path = string(shell)
	}
	dir, err := os.MkdirTemp("", "nexterm-shellintegr-*")
	if err != nil {
		return nil, err
	}
	launch := &Launch{Path: path, Cleanup: func() { os.RemoveAll(dir) }}
	fail := func(err error) (*Launch, error) {
		launch.Cleanup()
		return nil, err
	}
	write := func(name, body string) error {
		return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	}
	switch shell {
	case ShellBash:
		if err := write("bashrc", bashWrapper); err != nil {
			return fail(err)
		}
		launch.Args = []string{"--rcfile", filepath.Join(dir, "bashrc"), "-i"}
	case ShellZsh:
		files := [][2]string{
			{".zshenv", zshPassthrough(".zshenv")},
			{".zprofile", zshPassthrough(".zprofile")},
			{".zshrc", zshPassthrough(".zshrc") + zshIntegration},
			{".zlogin", zshPassthrough(".zlogin")},
		}
		for _, f := range files {
			if err := write(f[0], f[1]); err != nil {
				return fail(err)
			}
		}
		launch.Args = []string{"-i"}
		launch.Env = []string{"ZDOTDIR=" + dir}
	case ShellFish:
		if err := write("nexterm.fish", fishIntegration); err != nil {
			return fail(err)
		}
		launch.Args = []string{"-i", "--init-command", "source '" + filepath.Join(dir, "nexterm.fish") + "'"}
	default:
		return fail(fmt.Errorf("shellintegr: unsupported shell %q", shell))
	}
	return launch, nil
}

func zshPassthrough(name string) string {
	return "# nexterm shell integration\n[[ -r $HOME/" + name + " ]] && source $HOME/" + name + "\n"
}

const bashWrapper = `# nexterm shell integration
[[ -r $HOME/.bashrc ]] && source $HOME/.bashrc
__nexterm_host=${HOSTNAME:-$(hostname)}
__nexterm_osc7() {
    local out=${PWD//\%/%25}
    out=${out//\#/%23}
    out=${out//\?/%3F}
    out=${out// /%20}
    printf '\033]7;file://%s%s\033\\' "$__nexterm_host" "$out"
}
__nexterm_armed=0
__nexterm_preexec() {
    [ "$__nexterm_armed" = 1 ] || return 0
    __nexterm_armed=0
    printf '\033]133;B\033\\'
    printf '\033]133;C\033\\'
}
__nexterm_precmd() {
    local exit_code=$?
    printf '\033]133;D;%d\033\\' "$exit_code"
    printf '\033]133;A\033\\'
    __nexterm_osc7
}
__nexterm_arm() {
    __nexterm_armed=1
}
__nexterm_prev_debug=$(trap -p DEBUG)
if [ -n "$__nexterm_prev_debug" ]; then
    __nexterm_prev_debug=${__nexterm_prev_debug#trap -- \'}
    __nexterm_prev_debug=${__nexterm_prev_debug%\' DEBUG}
fi
__nexterm_debug_trap() {
    __nexterm_preexec
    if [ -n "$__nexterm_prev_debug" ]; then
        eval "$__nexterm_prev_debug"
    fi
}
trap '__nexterm_debug_trap' DEBUG
if ((BASH_VERSINFO[0] > 5 || (BASH_VERSINFO[0] == 5 && BASH_VERSINFO[1] >= 1))); then
    PROMPT_COMMAND=(__nexterm_precmd ${PROMPT_COMMAND+"${PROMPT_COMMAND[@]}"} __nexterm_arm)
else
    PROMPT_COMMAND="__nexterm_precmd${PROMPT_COMMAND:+;$PROMPT_COMMAND};__nexterm_arm"
fi
`

const zshIntegration = `__nexterm_host=${HOST:-$(hostname)}
__nexterm_osc7() {
    emulate -L zsh
    local out=${PWD//\%/%25}
    out=${out//\#/%23}
    out=${out//\?/%3F}
    out=${out// /%20}
    printf '\033]7;file://%s%s\033\\' "$__nexterm_host" "$out"
}
__nexterm_osc133_precmd() {
    printf '\033]133;D;%d\033\\' $?
    printf '\033]133;A\033\\'
    __nexterm_osc7
}
__nexterm_osc133_preexec() {
    printf '\033]133;B\033\\'
    printf '\033]133;C\033\\'
}
precmd_functions+=(__nexterm_osc133_precmd)
preexec_functions+=(__nexterm_osc133_preexec)
chpwd_functions+=(__nexterm_osc7)
`

const fishIntegration = `set -g __nexterm_host (hostname)
function __nexterm_osc7 --on-event fish_prompt --on-variable PWD
    set -l out (string escape --style=url $PWD)
    printf '\033]7;file://%s%s\033\\' $__nexterm_host $out
end
function __nexterm_osc133_prompt --on-event fish_prompt
    printf '\033]133;A\033\\'
end
function __nexterm_osc133_preexec --on-event fish_preexec
    printf '\033]133;B\033\\'
    printf '\033]133;C\033\\'
end
function __nexterm_osc133_postexec --on-event fish_postexec
    printf '\033]133;D;%d\033\\' $status
end
`
