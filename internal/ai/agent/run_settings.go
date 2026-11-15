package agent

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const (
	// MaxTurnsSettingKey stores the user's step limit for one assistant run.
	MaxTurnsSettingKey = "ai.max_turns"
	// DefaultMaxTurns applies when no valid limit is stored.
	DefaultMaxTurns = 999
	MinMaxTurns     = 1
	MaxMaxTurns     = 999
)

var ErrMaxTurnsRange = errors.New("单轮最大步数必须是 1 到 999 之间的整数")

// RunSettingsStore is the setting table subset the step limit is kept in.
type RunSettingsStore interface {
	SettingGet(context.Context, string) (string, bool, error)
	SettingSet(context.Context, string, string) error
}

// RunSettings are the user's assistant run options.
type RunSettings struct {
	MaxTurns int `json:"maxTurns"`
}

type RunSettingsInput struct {
	MaxTurns int `json:"maxTurns"`
}

// MaxTurnsSettingKeyForUser keeps each server account's limit apart; desktop and
// open access use the instance-wide key.
func MaxTurnsSettingKeyForUser(ctx context.Context) string {
	if userID, ok := ipc.UserIDFromContext(ctx); ok {
		return MaxTurnsSettingKey + ".user." + userID
	}
	return MaxTurnsSettingKey
}

func validMaxTurns(value int) bool {
	return value >= MinMaxTurns && value <= MaxMaxTurns
}

func parseMaxTurns(raw string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || !validMaxTurns(value) {
		return 0, false
	}
	return value, true
}

// maxTurns resolves the step limit when a run starts, so a saved change applies
// to the next run without a restart. A missing, unreadable or out-of-range
// stored value falls back to the configured default.
func (r *Runner) maxTurns(ctx context.Context) int {
	if r.config.Settings == nil {
		return r.config.MaxTurns
	}
	raw, found, err := r.config.Settings.SettingGet(ctx, MaxTurnsSettingKeyForUser(ctx))
	if err != nil {
		slog.Warn("read AI step limit failed; using default", "error", err)
		return r.config.MaxTurns
	}
	if !found {
		return r.config.MaxTurns
	}
	value, ok := parseMaxTurns(raw)
	if !ok {
		return r.config.MaxTurns
	}
	return value
}

func (r *Runner) runSettingsAccess(ctx context.Context) error {
	if r.config.Settings == nil {
		return ipc.NewError(ipc.CodeUnsupported, "AI 运行设置未配置")
	}
	if !r.config.ServerMode {
		return nil
	}
	if _, ok := ipc.UserIDFromContext(ctx); ok || owner.OpenAccess(ctx) {
		return nil
	}
	return ipc.NewError(ipc.CodeForbidden, "AI 请求缺少账号身份")
}

// RunSettings returns the step limit the next run of the caller will use.
func (r *Runner) RunSettings(ctx context.Context) (RunSettings, error) {
	if err := r.runSettingsAccess(ctx); err != nil {
		return RunSettings{}, err
	}
	return RunSettings{MaxTurns: r.maxTurns(ctx)}, nil
}

// SetRunSettings saves the caller's step limit after validating its range.
func (r *Runner) SetRunSettings(ctx context.Context, input RunSettingsInput) (RunSettings, error) {
	if err := r.runSettingsAccess(ctx); err != nil {
		return RunSettings{}, err
	}
	if !validMaxTurns(input.MaxTurns) {
		return RunSettings{}, ipc.WrapError(ipc.CodeBadParam, ErrMaxTurnsRange.Error(), ErrMaxTurnsRange)
	}
	if err := r.config.Settings.SettingSet(ctx, MaxTurnsSettingKeyForUser(ctx), strconv.Itoa(input.MaxTurns)); err != nil {
		return RunSettings{}, err
	}
	return RunSettings{MaxTurns: r.maxTurns(ctx)}, nil
}
