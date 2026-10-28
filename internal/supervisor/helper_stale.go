package supervisor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

// StaleAgainst reports whether the helper speaks this protocol but runs another build than local. Only a
// helper that reported the digest of its executable can be told apart; an unreported build is never
// called stale.
func (h RunningHelper) StaleAgainst(local RuntimeMetadata) bool {
	return h.Protocol == ProtocolVersion && h.Runtime != nil &&
		h.Runtime.SHA256 != "" && local.SHA256 != "" && h.Runtime.SHA256 != local.SHA256
}

// HelperBuild is the result of comparing the helper that serves a state directory with this build.
type HelperBuild struct {
	Helper RunningHelper
	Local  RuntimeMetadata
	// Stale is set when the helper runs an older build of the same protocol.
	Stale bool
	// Sessions is the number of live sessions the helper runs; SessionsKnown is false when it did not
	// report them.
	Sessions      int
	SessionsKnown bool
	// ConsentToken names exactly this helper and its sessions for ReplaceStaleHelper; empty unless Stale.
	ConsentToken string
	// Replaced is set by EnsureHelperBuild when it replaced a stale helper that ran no session.
	Replaced bool
}

// Notice is the text shown while a stale helper still runs sessions; empty when there is nothing to say.
func (b HelperBuild) Notice() string {
	if !b.Stale || !b.SessionsKnown || b.Sessions == 0 {
		return ""
	}
	return StaleHelperNotice(helperRelease(b.Helper.Runtime), helperRelease(&b.Local), b.Sessions)
}

func helperRelease(metadata *RuntimeMetadata) string {
	if metadata == nil || metadata.Version == "" {
		return "未知"
	}
	return metadata.Version
}

// StaleHelperNotice is the non-blocking notice for a host component of an older build that still runs
// persistent terminals.
func StaleHelperNotice(running, current string, sessions int) string {
	return fmt.Sprintf("主机端组件版本较旧（%s，当前 %s），仍有 %d 个持久终端在运行；全部结束后将自动更新。", running, current, sessions)
}

// InspectHelperBuild compares the helper serving stateDir with the running build without changing
// anything. ErrHelperNotRunning means no helper serves.
func InspectHelperBuild(ctx context.Context, stateDir string) (HelperBuild, error) {
	local, err := CurrentRuntimeMetadata()
	if err != nil {
		return HelperBuild{}, err
	}
	return inspectHelperBuild(ctx, stateDir, local)
}

func inspectHelperBuild(ctx context.Context, stateDir string, local RuntimeMetadata) (HelperBuild, error) {
	running, err := InspectHelper(ctx, stateDir)
	if err != nil {
		return HelperBuild{}, err
	}
	build := HelperBuild{Helper: running, Local: local, Stale: running.StaleAgainst(local)}
	build.SessionsKnown = running.ActivityKnown
	if running.ActivityKnown {
		build.Sessions = running.Activity.Total()
	}
	if build.Stale {
		build.ConsentToken = HelperConsentToken(running)
	}
	return build, nil
}

// EnsureHelperBuild replaces a helper of an older build when, and only when, it reports that it runs no
// session. A stale helper that still runs sessions is left untouched and returned for the caller to
// report; ending it needs ReplaceStaleHelper with the user's consent. A helper that is not running, or
// cannot be inspected, is not an error here.
func EnsureHelperBuild(ctx context.Context, config HelperConfig) (HelperBuild, error) {
	local, err := CurrentRuntimeMetadata()
	if err != nil {
		return HelperBuild{}, err
	}
	return ensureHelperBuild(ctx, config, local)
}

func ensureHelperBuild(ctx context.Context, config HelperConfig, local RuntimeMetadata) (HelperBuild, error) {
	build, err := inspectHelperBuild(ctx, config.StateDir, local)
	if err != nil {
		if errors.Is(err, ErrHelperNotRunning) {
			return HelperBuild{}, nil
		}
		return HelperBuild{}, err
	}
	if !build.Stale || !build.SessionsKnown || build.Sessions != 0 {
		return build, nil
	}
	if _, _, err := replaceStaleHelper(ctx, config, build.ConsentToken, local); err != nil {
		return build, err
	}
	build.Replaced = true
	return build, nil
}

// ReplaceStaleHelper ends the same-protocol helper of an older build that the consent token names, with
// all its sessions, and starts this build's helper. It follows ReplaceLegacyHelper: the helper is
// verified against the consent (process, state directory, no more sessions than agreed) and a helper
// that changed, or is no longer older, is refused with a fresh *LegacyHelperError before anything is
// signalled.
func ReplaceStaleHelper(ctx context.Context, config HelperConfig, token string) (*Helper, ReplaceResult, error) {
	local, err := CurrentRuntimeMetadata()
	if err != nil {
		return nil, ReplaceResult{}, err
	}
	return replaceStaleHelper(ctx, config, token, local)
}

func replaceStaleHelper(ctx context.Context, config HelperConfig, token string, local RuntimeMetadata) (*Helper, ReplaceResult, error) {
	consent, ok := ParseHelperConsent(token)
	if !ok || consent.Protocol != ProtocolVersion {
		return nil, ReplaceResult{}, fmt.Errorf("%w: malformed helper replacement consent", ErrInvalidInput)
	}
	stateDir, err := filepath.Abs(config.StateDir)
	if err != nil || config.StateDir == "" {
		return nil, ReplaceResult{}, fmt.Errorf("%w: helper state directory is required", ErrInvalidInput)
	}
	config.StateDir = stateDir
	config.NoSpawn = false
	unlock, err := waitReplaceLock(ctx, stateDir)
	if err != nil {
		return nil, ReplaceResult{}, &HelperReplaceError{Stage: ReplaceVerify, Cause: err}
	}
	defer unlock()
	steps := replaceSteps{
		verify: func(ctx context.Context, protocol int) (RunningHelper, error) {
			running, err := inspectHelperVersion(ctx, stateDir, protocol)
			if errors.Is(err, errNoListener) {
				return RunningHelper{}, fmt.Errorf("%w: %v", ErrHelperNotRunning, err)
			}
			return running, err
		},
		stop: func(ctx context.Context, expected RunningHelper) (StopResult, error) {
			return stopHelper(ctx, stateDir, expected)
		},
		start: func(ctx context.Context) (*Helper, error) {
			return ConnectHelper(ctx, config)
		},
		restore: func(ctx context.Context, executable string, protocol int) (bool, error) {
			return restoreHelper(ctx, stateDir, executable, protocol)
		},
		sameProtocol: true,
		stillOlder:   func(running RunningHelper) bool { return running.StaleAgainst(local) },
	}
	helper, result, _, err := steps.run(ctx, consent)
	return helper, result, err
}
