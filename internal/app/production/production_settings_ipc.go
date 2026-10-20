package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// Per-owner production safety configuration: the configurable production
// environment list plus the R-07 connect opt-ins for the cron, broadcast, and
// target-set surfaces. All values default to off/empty and are persisted per
// user through the existing guard/settings keys — this file adds no second
// persistence source.
const (
	maxProductionEnvironmentNames  = 32
	maxProductionEnvironmentLength = 64
)

type productionSettingsDTO struct {
	ProductionEnvironments          []string `json:"productionEnvironments"`
	CronConnectOptIn                bool     `json:"cronConnectOptIn"`
	CronConnectProductionOptIn      bool     `json:"cronConnectProductionOptIn"`
	BroadcastConnectOptIn           bool     `json:"broadcastConnectOptIn"`
	BroadcastConnectProductionOptIn bool     `json:"broadcastConnectProductionOptIn"`
	TargetSetConnectOptIn           bool     `json:"targetSetConnectOptIn"`
	TargetSetConnectProductionOptIn bool     `json:"targetSetConnectProductionOptIn"`
}

// productionSettingsUpdateRequest patches only the provided fields.
type productionSettingsUpdateRequest struct {
	ProductionEnvironments          *[]string `json:"productionEnvironments,omitempty"`
	CronConnectOptIn                *bool     `json:"cronConnectOptIn,omitempty"`
	CronConnectProductionOptIn      *bool     `json:"cronConnectProductionOptIn,omitempty"`
	BroadcastConnectOptIn           *bool     `json:"broadcastConnectOptIn,omitempty"`
	BroadcastConnectProductionOptIn *bool     `json:"broadcastConnectProductionOptIn,omitempty"`
	TargetSetConnectOptIn           *bool     `json:"targetSetConnectOptIn,omitempty"`
	TargetSetConnectProductionOptIn *bool     `json:"targetSetConnectProductionOptIn,omitempty"`
}

type productionSettingsStore interface {
	SettingGet(context.Context, string) (string, bool, error)
	SettingSet(context.Context, string, string) error
}

func registerProductionSettingsCommands(dispatcher *ipc.Dispatcher, database productionSettingsStore) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "production_settings_get", func(ctx context.Context, _ *ipc.Call, _ struct{}) (productionSettingsDTO, error) {
				return loadProductionSettings(ctx, database)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "production_settings_set", func(ctx context.Context, _ *ipc.Call, input productionSettingsUpdateRequest) (productionSettingsDTO, error) {
				if err := applyProductionSettingsUpdate(ctx, database, input); err != nil {
					return productionSettingsDTO{}, err
				}
				return loadProductionSettings(ctx, database)
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

func loadProductionSettings(ctx context.Context, database productionSettingsStore) (productionSettingsDTO, error) {
	var result productionSettingsDTO
	if database == nil {
		return result, ipc.NewError(ipc.CodeUnsupported, "设置存储未配置")
	}
	environments, err := guard.ConfiguredProductionEnvironments(ctx, database)
	if err != nil {
		return productionSettingsDTO{}, err
	}
	// The generated DTO declares a string array; a missing or cleared setting
	// must marshal as [] rather than null at the API boundary.
	if environments == nil {
		environments = []string{}
	}
	result.ProductionEnvironments = environments
	for key, target := range map[string]*bool{
		cron.ConnectOptInSettingKeyForUser(ctx):               &result.CronConnectOptIn,
		cron.ConnectProductionOptInSettingKeyForUser(ctx):     &result.CronConnectProductionOptIn,
		BroadcastConnectOptInSettingKeyForUser(ctx):           &result.BroadcastConnectOptIn,
		BroadcastConnectProductionOptInSettingKeyForUser(ctx): &result.BroadcastConnectProductionOptIn,
		TargetSetConnectOptInSettingKeyForUser(ctx):           &result.TargetSetConnectOptIn,
		TargetSetConnectProductionOptInSettingKeyForUser(ctx): &result.TargetSetConnectProductionOptIn,
	} {
		optIn, err := cronConnectSetting(ctx, database, key)
		if err != nil {
			return productionSettingsDTO{}, err
		}
		*target = optIn
	}
	return result, nil
}

func applyProductionSettingsUpdate(ctx context.Context, database productionSettingsStore, input productionSettingsUpdateRequest) error {
	if database == nil {
		return ipc.NewError(ipc.CodeUnsupported, "设置存储未配置")
	}
	if input.ProductionEnvironments != nil {
		names, err := validateProductionEnvironmentNames(*input.ProductionEnvironments)
		if err != nil {
			return err
		}
		if err := guard.SetProductionEnvironments(ctx, database, names); err != nil {
			return err
		}
	}
	for key, value := range map[string]*bool{
		cron.ConnectOptInSettingKeyForUser(ctx):               input.CronConnectOptIn,
		cron.ConnectProductionOptInSettingKeyForUser(ctx):     input.CronConnectProductionOptIn,
		BroadcastConnectOptInSettingKeyForUser(ctx):           input.BroadcastConnectOptIn,
		BroadcastConnectProductionOptInSettingKeyForUser(ctx): input.BroadcastConnectProductionOptIn,
		TargetSetConnectOptInSettingKeyForUser(ctx):           input.TargetSetConnectOptIn,
		TargetSetConnectProductionOptInSettingKeyForUser(ctx): input.TargetSetConnectProductionOptIn,
	} {
		if value == nil {
			continue
		}
		encoded, err := json.Marshal(*value)
		if err != nil {
			return err
		}
		if err := database.SettingSet(ctx, key, string(encoded)); err != nil {
			return err
		}
	}
	return nil
}

// validateProductionEnvironmentNames rejects entries that are empty after
// trimming, exceed the length cap, or exceed the count cap: invalid input
// fails closed instead of being silently normalized away.
func validateProductionEnvironmentNames(values []string) ([]string, error) {
	if len(values) > maxProductionEnvironmentNames {
		return nil, ipc.BadParam(fmt.Errorf("生产环境名最多 %d 个", maxProductionEnvironmentNames))
	}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil, ipc.BadParam(errors.New("生产环境名不能为空"))
		}
		if len(trimmed) > maxProductionEnvironmentLength {
			return nil, ipc.BadParam(fmt.Errorf("生产环境名最长 %d 字符", maxProductionEnvironmentLength))
		}
	}
	return values, nil
}
