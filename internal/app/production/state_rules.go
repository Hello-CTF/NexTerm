package production

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/atomicfile"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/terminal/staterule"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// stateRuleStore is the part of the store the state rule runtime uses.
type stateRuleStore interface {
	StateRuleList(ctx context.Context) ([]store.StateRuleRow, error)
	StateRuleSave(ctx context.Context, input store.StateRuleInput) (store.StateRuleRow, error)
	StateRuleDelete(ctx context.Context, id string) error
	StateRuleReorder(ctx context.Context, order []string) ([]store.StateRuleRow, error)
	StateRuleImport(ctx context.Context, inputs []store.StateRuleInput) (store.StateRuleImportResult, error)
	StateRulesFor(ctx context.Context, assetID string) ([]staterule.Rule, []string, error)
	StateRuleFingerprint(ctx context.Context) (store.StateRuleFingerprint, error)
}

// stateRulePushTimeout bounds one push to a helper after a rule change.
const stateRulePushTimeout = 20 * time.Second

// Helper rule status values reported by state_rule_status.
const (
	stateRuleStatusApplied = "applied"
	// pending: the helper could not be reached; it takes the rules with its
	// next connection.
	stateRuleStatusPending = "pending"
	stateRuleStatusFailed  = "failed"
	// unsupported: the helper is of a build without state rules.
	stateRuleStatusUnsupported = "unsupported"
)

// stateRulesIdentityName is the file in the data directory that holds this
// installation's random state rule identity.
const stateRulesIdentityName = "state-rules-identity"

// loadStateRulesIdentity reads this installation's state rule identity,
// creating it on first use. Without a readable data directory it returns a
// fresh identity for this run only.
func loadStateRulesIdentity(dataDir string, logger *slog.Logger) string {
	path := filepath.Join(dataDir, stateRulesIdentityName)
	if data, err := os.ReadFile(path); err == nil {
		if identity := strings.TrimSpace(string(data)); len(identity) == 64 {
			if _, err := hex.DecodeString(identity); err == nil {
				return identity
			}
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("read random state rule identity: %v", err))
	}
	identity := hex.EncodeToString(raw)
	if err := atomicfile.Write(path, 0o600, func(writer io.Writer) error {
		_, err := io.WriteString(writer, identity+"\n")
		return err
	}); err != nil {
		logger.Warn("persist the state rule identity; helpers will not recognize this run's rule owners after a restart", "error", err)
	}
	return identity
}

// stateRulesOwnerKey is the opaque key a helper knows a rule owner by: a
// hash of this installation's identity and the account, so the account ID
// never leaves the app and owners of different installations or accounts
// sharing one helper stay apart.
func stateRulesOwnerKey(identity, account string) string {
	sum := sha256.Sum256([]byte("nexterm-state-rules\x00" + identity + "\x00" + account))
	return hex.EncodeToString(sum[:16])
}

// stateRulesRuntime keeps durable helpers on their owner's state rules. Every
// helper provider the app resolves is wrapped so it acts for the owner's
// rule key: the sessions it creates are evaluated with that owner's rules,
// and each connection pushes the rules after the hello when the helper holds
// another revision. A rule change pushes to every known helper of the owner
// at once, so a helper online takes a change with one push.
//
// A host-scoped rule applies to sessions created through that asset: the
// set pushed for a provider holds the global rules and those of the asset it
// was resolved for.
type stateRulesRuntime struct {
	store    stateRuleStore
	logger   *slog.Logger
	identity string
	// multiAccount refuses helpers without an account: one set would
	// otherwise serve every account.
	multiAccount bool
	// draftModel drafts rules from a screen; nil without the AI runtime.
	draftModel stateRuleModelFactory

	mu      sync.Mutex
	helpers map[stateRuleHelperKey]*stateRuleHelper
}

type stateRuleHelperKey struct {
	owner string
	asset string
}

func newStateRulesRuntime(database stateRuleStore, logger *slog.Logger, identity string, multiAccount bool) *stateRulesRuntime {
	if logger == nil {
		logger = slog.Default()
	}
	return &stateRulesRuntime{
		store: database, logger: logger, identity: identity, multiAccount: multiAccount,
		helpers: make(map[stateRuleHelperKey]*stateRuleHelper),
	}
}

// stateRuleHelper is the rule source of one owner's helper on one host.
type stateRuleHelper struct {
	runtime  *stateRulesRuntime
	key      stateRuleHelperKey
	ownerKey string

	mu sync.Mutex
	// sync pushes the rules now; nil until a provider is registered.
	sync func(context.Context) error
	// cached is the set built for fingerprint; valid while cacheOK.
	cached      supervisor.StateRuleSet
	fingerprint store.StateRuleFingerprint
	cacheOK     bool
	revision    string
	status      string
	err         string
	skipped     []string
	at          time.Time
	pushing     bool
	again       bool
}

func (h *stateRuleHelper) ownerContext() context.Context {
	ctx := context.Background()
	if h.key.owner != "" {
		ctx = ipc.WithUserID(ctx, h.key.owner)
	}
	return ctx
}

func (h *stateRuleHelper) StateRulesOwner() string { return h.ownerKey }

// StateRules returns the set for this helper, built from the store and kept
// until the owner's rules change. A store failure keeps the helper on the
// rules it has. An owner without rules gets an empty set, which is pushed
// only to clear rules the helper still holds for this owner key.
func (h *stateRuleHelper) StateRules() (supervisor.StateRuleSet, bool) {
	ctx := h.ownerContext()
	fingerprint, err := h.runtime.store.StateRuleFingerprint(ctx)
	if err != nil {
		h.runtime.logger.Warn("read state rules for a helper", "asset", h.key.asset, "error", err)
		return supervisor.StateRuleSet{}, false
	}
	h.mu.Lock()
	if h.cacheOK && h.fingerprint == fingerprint {
		set := h.cached
		h.mu.Unlock()
		return set, true
	}
	h.mu.Unlock()
	rules, skipped, err := h.runtime.store.StateRulesFor(ctx, h.key.asset)
	if err != nil {
		h.runtime.logger.Warn("read state rules for a helper", "asset", h.key.asset, "error", err)
		return supervisor.StateRuleSet{}, false
	}
	set, err := staterule.NewSet(rules)
	if err != nil {
		h.record("", err)
		h.runtime.logger.Warn("state rules for a helper do not form a valid set", "asset", h.key.asset, "error", err)
		return supervisor.StateRuleSet{}, false
	}
	h.mu.Lock()
	h.cached, h.fingerprint, h.cacheOK, h.skipped = set, fingerprint, true, skipped
	h.mu.Unlock()
	return set, true
}

func (h *stateRuleHelper) StateRulesPushed(revision string, err error) {
	h.record(revision, err)
}

func (h *stateRuleHelper) record(revision string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.at = time.Now()
	switch {
	case err == nil:
		h.revision, h.status, h.err = revision, stateRuleStatusApplied, ""
	case errors.Is(err, supervisor.ErrUnsupported):
		h.status, h.err = stateRuleStatusUnsupported, err.Error()
	case unreachableHelper(err):
		h.status, h.err = stateRuleStatusPending, err.Error()
	default:
		h.status, h.err = stateRuleStatusFailed, err.Error()
	}
}

func unreachableHelper(err error) bool {
	return errors.Is(err, supervisor.ErrUnavailable) || errors.Is(err, base.ErrDisconnected) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// invalidate drops the cached set.
func (h *stateRuleHelper) invalidate() {
	h.mu.Lock()
	h.cacheOK = false
	h.mu.Unlock()
}

// push runs sync, coalescing pushes requested while one is in flight.
func (h *stateRuleHelper) push() {
	h.mu.Lock()
	if h.sync == nil {
		h.mu.Unlock()
		return
	}
	if h.pushing {
		h.again = true
		h.mu.Unlock()
		return
	}
	h.pushing = true
	sync := h.sync
	h.mu.Unlock()
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), stateRulePushTimeout)
			if err := sync(ctx); err != nil {
				h.runtime.logger.Debug("push state rules to a helper", "asset", h.key.asset, "error", err)
			}
			cancel()
			h.mu.Lock()
			if !h.again {
				h.pushing = false
				h.mu.Unlock()
				return
			}
			h.again = false
			h.mu.Unlock()
		}
	}()
}

// helper returns the rule source of owner's helper on asset, creating it.
func (r *stateRulesRuntime) helper(owner, asset string) *stateRuleHelper {
	key := stateRuleHelperKey{owner: owner, asset: asset}
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.helpers[key]
	if h == nil {
		h = &stateRuleHelper{runtime: r, key: key, ownerKey: stateRulesOwnerKey(r.identity, owner)}
		r.helpers[key] = h
	}
	return h
}

// wrapProvider makes a resolved durable provider act for the rule owner in
// ctx on assetID. Providers that do not speak to a helper, and on a
// multi-account server providers resolved without an account, are returned
// unchanged: their sessions use no rules.
func (r *stateRulesRuntime) wrapProvider(ctx context.Context, assetID string, provider base.DurableProvider) base.DurableProvider {
	remote, ok := provider.(*supervisor.RemoteProvider)
	if r == nil || !ok || remote == nil {
		return provider
	}
	owner := ipc.OwnerID(ctx)
	if owner == "" && r.multiAccount {
		r.logger.Warn("durable helper resolved without an account; its sessions use no state rules", "asset", assetID)
		return provider
	}
	h := r.helper(owner, assetID)
	wrapped := remote.WithStateRules(h)
	h.mu.Lock()
	h.sync = wrapped.SyncStateRules
	h.mu.Unlock()
	return wrapped
}

// localSource is the rule source of the local helper: the desktop or the
// single account's rules for the built-in local device.
func (r *stateRulesRuntime) localSource() supervisor.StateRuleSource {
	if r == nil {
		return nil
	}
	return r.helper("", store.BuiltinLocalAssetID)
}

// bindLocal lets rule changes push to the local helper.
func (r *stateRulesRuntime) bindLocal(sync func(context.Context) error) {
	if r == nil || sync == nil {
		return
	}
	h := r.helper("", store.BuiltinLocalAssetID)
	h.mu.Lock()
	h.sync = sync
	h.mu.Unlock()
}

// owns reports whether a helper registered for key.owner serves owner. A
// helper registered without an account serves the desktop or the single
// account, so it serves any owner except on a multi-account server.
func (r *stateRulesRuntime) owns(key stateRuleHelperKey, owner string) bool {
	return key.owner == owner || key.owner == "" && !r.multiAccount
}

// changed pushes the rules to every known helper of owner.
func (r *stateRulesRuntime) changed(owner string) {
	r.mu.Lock()
	targets := make([]*stateRuleHelper, 0, len(r.helpers))
	for key, h := range r.helpers {
		if r.owns(key, owner) {
			targets = append(targets, h)
		}
	}
	r.mu.Unlock()
	for _, h := range targets {
		h.invalidate()
		h.push()
	}
}

// stateRuleDTO is a stored rule as the UI and exports see it.
type stateRuleDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	AssetID   string `json:"assetId,omitempty"`
	Region    string `json:"region"`
	Lines     int32  `json:"lines,omitempty"`
	Pattern   string `json:"pattern"`
	State     string `json:"state"`
	Keys      string `json:"keys,omitempty"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"createdAt,omitempty"`
	UpdatedAt int64  `json:"updatedAt"`
}

func stateRuleDTOFromRow(row store.StateRuleRow) stateRuleDTO {
	return stateRuleDTO{
		ID: row.ID, Name: row.Name, Scope: row.Scope, AssetID: row.AssetID, Region: row.Region,
		Lines: row.RegionLines, Pattern: row.Pattern, State: row.TargetState, Keys: row.KeySequence,
		Enabled: row.Enabled, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

type stateRuleSaveRequest struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Scope   string `json:"scope"`
	AssetID string `json:"assetId,omitempty"`
	Region  string `json:"region"`
	Lines   int32  `json:"lines,omitempty"`
	Pattern string `json:"pattern"`
	State   string `json:"state"`
	Keys    string `json:"keys,omitempty"`
	Enabled bool   `json:"enabled"`
}

type stateRuleReorderRequest struct {
	// IDs lists every rule of the owner in the new order.
	IDs []string `json:"ids"`
}

type stateRuleValidateRequest struct {
	Rule stateRuleSaveRequest `json:"rule"`
	// Screen, when set, is matched against the rule.
	Screen *stateRuleScreenDTO `json:"screen,omitempty"`
}

// stateRuleValidateResult says whether a rule would be accepted and, for a
// screen, whether it matches and what text it sees there.
type stateRuleValidateResult struct {
	Valid bool `json:"valid"`
	// Field names the invalid field: name, scope, region, lines, pattern,
	// state or keys; empty for an error not about one field.
	Field string `json:"field,omitempty"`
	Error string `json:"error,omitempty"`
	// Lines is the normalized row count of a valid bottom rule.
	Lines   int32  `json:"lines,omitempty"`
	Tested  bool   `json:"tested"`
	Matched bool   `json:"matched"`
	Region  string `json:"region,omitempty"`
}

// validateStateRule checks a rule as state_rule_save would, except for the
// checks against stored data (the host and the owner's limits), and tests
// it against the screen when one is given.
func validateStateRule(request stateRuleValidateRequest) stateRuleValidateResult {
	input := request.Rule
	normalized, err := store.ValidateStateRuleInput(store.StateRuleInput{
		ID: input.ID, Name: input.Name, Scope: input.Scope, AssetID: input.AssetID, Region: input.Region,
		Lines: input.Lines, Pattern: input.Pattern, State: input.State, Keys: input.Keys, Enabled: input.Enabled,
	})
	if err != nil {
		return stateRuleValidateResult{Error: ipc.NormalizeError(err).Message, Field: stateRuleInvalidField(input)}
	}
	result := stateRuleValidateResult{Valid: true, Lines: normalized.Lines}
	if request.Screen == nil {
		return result
	}
	var compiled *staterule.Compiled
	rule := staterule.Rule{
		ID: normalized.ID, Region: normalized.Region, Lines: int(normalized.Lines),
		Pattern: normalized.Pattern, State: normalized.State, Keys: normalized.Keys,
	}
	set, err := staterule.NewSet([]staterule.Rule{rule})
	if err == nil {
		compiled, err = staterule.Compile(set)
	}
	if err != nil {
		return stateRuleValidateResult{Error: fmt.Sprintf("匹配模式过于复杂或区域行数过多: %v", err), Field: "pattern"}
	}
	screen := request.Screen.screen()
	_, result.Matched = compiled.Match(screen)
	result.Tested = true
	result.Region = staterule.RegionText(screen, rule)
	return result
}

// stateRuleInvalidField names the field of an invalid rule in the order
// store.ValidateStateRuleInput checks them.
func stateRuleInvalidField(input stateRuleSaveRequest) string {
	if utf8.RuneCountInString(strings.TrimSpace(input.Name)) > store.MaxStateRuleNameRunes {
		return "name"
	}
	switch input.Scope {
	case store.StateRuleScopeGlobal:
	case store.StateRuleScopeHost:
		if strings.TrimSpace(input.AssetID) == "" {
			return "scope"
		}
	default:
		return "scope"
	}
	rule := staterule.Normalize(staterule.Rule{
		ID: ids.New(), Region: input.Region, Lines: int(input.Lines), Pattern: input.Pattern, State: input.State, Keys: input.Keys,
	})
	var typed *staterule.Error
	if _, err := staterule.Validate(rule); errors.As(err, &typed) {
		return typed.Field
	}
	return ""
}

type stateRuleDeleteRequest struct {
	ID string `json:"id"`
}

type stateRuleDeleteResult struct {
	ID string `json:"id"`
}

// stateRuleExportFormat names the export file format.
const (
	stateRuleExportFormat  = "nexterm.state-rules"
	stateRuleExportVersion = 1
)

type stateRuleExportDTO struct {
	Format  string         `json:"format"`
	Version int            `json:"version"`
	Rules   []stateRuleDTO `json:"rules"`
}

type stateRuleImportRequest struct {
	Format  string         `json:"format"`
	Version int            `json:"version"`
	Rules   []stateRuleDTO `json:"rules"`
}

type stateRuleImportResultDTO struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
}

// stateRuleHelperStatusDTO reports whether a helper runs the current rules.
type stateRuleHelperStatusDTO struct {
	AssetID string `json:"assetId"`
	// Status is applied, pending (unreachable; it takes the rules with its
	// next connection) or failed.
	Status   string   `json:"status"`
	Revision string   `json:"revision,omitempty"`
	Error    string   `json:"error,omitempty"`
	Skipped  []string `json:"skipped,omitempty"`
	At       int64    `json:"at,omitempty"`
}

func (r *stateRulesRuntime) status(owner string) []stateRuleHelperStatusDTO {
	r.mu.Lock()
	helpers := make([]*stateRuleHelper, 0, len(r.helpers))
	for key, h := range r.helpers {
		if r.owns(key, owner) {
			helpers = append(helpers, h)
		}
	}
	r.mu.Unlock()
	result := make([]stateRuleHelperStatusDTO, 0, len(helpers))
	for _, h := range helpers {
		h.mu.Lock()
		if h.status != "" {
			entry := stateRuleHelperStatusDTO{
				AssetID: h.key.asset, Status: h.status, Revision: h.revision, Error: h.err,
				Skipped: append([]string(nil), h.skipped...), At: h.at.UnixMilli(),
			}
			result = append(result, entry)
		}
		h.mu.Unlock()
	}
	sort.Slice(result, func(i, j int) bool { return result[i].AssetID < result[j].AssetID })
	return result
}

func (r *stateRulesRuntime) registerCommands(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "state_rule_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]stateRuleDTO, error) {
				rows, err := r.store.StateRuleList(ctx)
				if err != nil {
					return nil, err
				}
				result := make([]stateRuleDTO, len(rows))
				for i, row := range rows {
					result[i] = stateRuleDTOFromRow(row)
				}
				return result, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "state_rule_save", func(ctx context.Context, _ *ipc.Call, input stateRuleSaveRequest) (stateRuleDTO, error) {
				row, err := r.store.StateRuleSave(ctx, store.StateRuleInput{
					ID: input.ID, Name: input.Name, Scope: input.Scope, AssetID: input.AssetID, Region: input.Region,
					Lines: input.Lines, Pattern: input.Pattern, State: input.State, Keys: input.Keys, Enabled: input.Enabled,
				})
				if err != nil {
					return stateRuleDTO{}, err
				}
				r.changed(ipc.OwnerID(ctx))
				return stateRuleDTOFromRow(row), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "state_rule_delete", func(ctx context.Context, _ *ipc.Call, input stateRuleDeleteRequest) (stateRuleDeleteResult, error) {
				if err := r.store.StateRuleDelete(ctx, input.ID); err != nil {
					return stateRuleDeleteResult{}, err
				}
				r.changed(ipc.OwnerID(ctx))
				return stateRuleDeleteResult{ID: input.ID}, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "state_rule_export", func(ctx context.Context, _ *ipc.Call, _ struct{}) (stateRuleExportDTO, error) {
				rows, err := r.store.StateRuleList(ctx)
				if err != nil {
					return stateRuleExportDTO{}, err
				}
				export := stateRuleExportDTO{Format: stateRuleExportFormat, Version: stateRuleExportVersion, Rules: make([]stateRuleDTO, len(rows))}
				for i, row := range rows {
					rule := stateRuleDTOFromRow(row)
					rule.CreatedAt = 0
					export.Rules[i] = rule
				}
				return export, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "state_rule_import", func(ctx context.Context, _ *ipc.Call, input stateRuleImportRequest) (stateRuleImportResultDTO, error) {
				if input.Format != stateRuleExportFormat || input.Version != stateRuleExportVersion {
					return stateRuleImportResultDTO{}, ipc.BadParam(fmt.Errorf("不是可识别的状态规则文件（格式 %q，版本 %d）", input.Format, input.Version))
				}
				inputs := make([]store.StateRuleInput, len(input.Rules))
				for i, rule := range input.Rules {
					inputs[i] = store.StateRuleInput{
						ID: rule.ID, Name: rule.Name, Scope: rule.Scope, AssetID: rule.AssetID, Region: rule.Region,
						Lines: rule.Lines, Pattern: rule.Pattern, State: rule.State, Keys: rule.Keys,
						Enabled: rule.Enabled, UpdatedAt: rule.UpdatedAt,
					}
				}
				result, err := r.store.StateRuleImport(ctx, inputs)
				if err != nil {
					return stateRuleImportResultDTO{}, err
				}
				if result.Created+result.Updated > 0 {
					r.changed(ipc.OwnerID(ctx))
				}
				return stateRuleImportResultDTO{Created: result.Created, Updated: result.Updated, Unchanged: result.Unchanged}, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "state_rule_reorder", func(ctx context.Context, _ *ipc.Call, input stateRuleReorderRequest) ([]stateRuleDTO, error) {
				rows, err := r.store.StateRuleReorder(ctx, input.IDs)
				if err != nil {
					return nil, err
				}
				r.changed(ipc.OwnerID(ctx))
				result := make([]stateRuleDTO, len(rows))
				for i, row := range rows {
					result[i] = stateRuleDTOFromRow(row)
				}
				return result, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "state_rule_validate", func(_ context.Context, _ *ipc.Call, input stateRuleValidateRequest) (stateRuleValidateResult, error) {
				return validateStateRule(input), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "state_rule_draft", func(ctx context.Context, _ *ipc.Call, input stateRuleDraftRequest) (stateRuleDraftDTO, error) {
				return r.draft(ctx, input)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "state_rule_status", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]stateRuleHelperStatusDTO, error) {
				return r.status(ipc.OwnerID(ctx)), nil
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
