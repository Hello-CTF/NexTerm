package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/Hello-CTF/NexTerm/internal/outcome"
)

type Registry struct {
	deps    Dependencies
	mu      sync.Mutex
	jobs    map[string]*jobState
	schemas []Schema
	// flights arbitrates recorded operations in-process across
	// adjudication, any pre-key connect, and the ledger reserve/claim, so a
	// concurrent duplicate of the same call fails fast instead of
	// establishing a second connection.
	flights map[string]struct{}
}

func NewRegistry(deps Dependencies) *Registry {
	schemas := toolSchemas()
	if skillLoaderConfigured(deps.Skills) {
		schemas = append(schemas, deps.Skills.LoadSkillSchema())
	}
	deps = composeAssetIdentityResolvers(deps)
	return &Registry{deps: deps, jobs: make(map[string]*jobState), schemas: schemas, flights: make(map[string]struct{})}
}

// composeAssetIdentityResolvers wires the identity-aware resolvers a fully
// configured dependency set implies but did not spell out. The live
// session's bound asset identity (asset ID plus config fingerprint) is the
// same source the execution-time re-validation reads, so a registry with
// the plain session resolvers and an identity-capable terminal binds and
// re-validates real identities instead of marking operations unverifiable.
// Explicitly configured resolvers always win, a terminal without the
// identity seam keeps the honest unverifiable path, and a session without
// an identity source (device, inline) resolves to
// ErrAssetIdentityUnverifiable so the operation stays visibly unverifiable
// instead of binding an invented identity.
func composeAssetIdentityResolvers(deps Dependencies) Dependencies {
	identities, ok := deps.Terminal.(SessionAssetIdentityResolver)
	if !ok {
		return deps
	}
	if deps.ResolveAssetSessionIdentity == nil && deps.ResolveAssetSession != nil {
		resolve := deps.ResolveAssetSession
		deps.ResolveAssetSessionIdentity = func(ctx context.Context, assetID string) (string, AssetIdentity, error) {
			sessionID, err := resolve(ctx, assetID)
			if err != nil {
				return "", AssetIdentity{}, err
			}
			identity, err := identities.SessionAssetIdentity(ctx, sessionID)
			if err != nil {
				return "", AssetIdentity{}, err
			}
			if identity.AssetID == "" || identity.ConfigFingerprint == "" {
				// A session without an identity source (device, inline)
				// resolves to the honest unverifiable verdict — carrying the
				// resolved session with it, never an invented identity.
				return sessionID, AssetIdentity{}, ErrAssetIdentityUnverifiable
			}
			return sessionID, identity, nil
		}
	}
	if deps.ResolveExecTargetIdentities == nil && deps.ResolveExecTargets != nil {
		resolve := deps.ResolveExecTargets
		deps.ResolveExecTargetIdentities = func(ctx context.Context, assetIDs, tags []string) (ExecTargetSet, []AssetIdentity, error) {
			targets, err := resolve(ctx, assetIDs, tags)
			if err != nil {
				return ExecTargetSet{}, nil, err
			}
			resolved := make([]AssetIdentity, 0, len(targets.SessionIDs))
			for _, sessionID := range targets.SessionIDs {
				identity, err := identities.SessionAssetIdentity(ctx, sessionID)
				if err != nil {
					return ExecTargetSet{}, nil, err
				}
				if identity.AssetID == "" || identity.ConfigFingerprint == "" {
					// One unevidenced target unbinds the whole batch: it runs
					// on the honest unverifiable path instead — carrying the
					// resolved targets with the verdict.
					return targets, nil, ErrAssetIdentityUnverifiable
				}
				resolved = append(resolved, identity)
			}
			return targets, resolved, nil
		}
	}
	return deps
}

// acquireOperationFlight marks the operation in-flight until the returned
// release runs; a busy operation reports acquired=false.
func (r *Registry) acquireOperationFlight(key string) (release func(), acquired bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, busy := r.flights[key]; busy {
		return nil, false
	}
	r.flights[key] = struct{}{}
	return func() {
		r.mu.Lock()
		delete(r.flights, key)
		r.mu.Unlock()
	}, true
}

func PlanAllowed(name string) bool {
	switch name {
	case "read_file", "list_dir", "search_files", "read_screen", "wait_for", "terminal_list", "operation_result", "list_assets", "docker_ps", "docker_logs", "db_list_tables", "db_describe", "redis_scan", "ask_user", "todo_write", "exit_plan_mode", "load_skill", NotifyTool:
		return true
	default:
		return false
	}
}

func (r *Registry) Release(jobID string) {
	r.mu.Lock()
	delete(r.jobs, jobID)
	r.mu.Unlock()
}

func (r *Registry) state(jobID string) *jobState {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.jobs[jobID]
	if state == nil {
		state = newJobState()
		r.jobs[jobID] = state
	}
	return state
}

func ValidateCall(schema Schema, args json.RawMessage) error {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return invalid("JSON: %v", err)
	}
	if decoder.More() {
		return invalid("JSON 后存在多余内容")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return invalid("参数必须是 JSON 对象")
	}
	properties, _ := schema.Parameters["properties"].(map[string]any)
	for key := range object {
		if _, ok := properties[key]; !ok {
			return invalid("未知参数 %s", key)
		}
	}
	required := requiredStrings(schema.Parameters["required"])
	for _, key := range required {
		if _, ok := object[key]; !ok {
			return invalid("缺少必填参数 %s", key)
		}
	}
	for key, raw := range object {
		definition, _ := properties[key].(map[string]any)
		kind, _ := definition["type"].(string)
		if !validJSONType(raw, kind) {
			return invalid("参数 %s 应为 %s", key, kind)
		}
		if enum, ok := definition["enum"].([]any); ok {
			text, _ := raw.(string)
			valid := false
			for _, candidate := range enum {
				valid = valid || text == candidate
			}
			if !valid {
				return invalid("参数 %s 的值 %q 不在允许范围", key, text)
			}
		}
	}
	return nil
}

func requiredStrings(value any) []string {
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func validJSONType(value any, kind string) bool {
	switch kind {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Float64()
		return err == nil
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	default:
		return true
	}
}

func (r *Registry) Validate(call Call) error {
	for _, schema := range r.schemas {
		if schema.Name == call.Name {
			return ValidateCall(schema, call.Args)
		}
	}
	return invalid("未知工具 %s", call.Name)
}

func (r *Registry) Prepare(ctx context.Context, jobID string, scope Scope, call Call) (*Preparation, error) {
	if err := r.Validate(call); err != nil {
		return nil, err
	}
	if call.Name != "write_file" && call.Name != "edit_file" {
		return &Preparation{}, nil
	}
	change, preview, err := r.prepareFile(ctx, jobID, scope, call)
	if err != nil {
		return nil, err
	}
	if change != nil {
		r.state(jobID).putPreparation(call.ID, change)
	}
	return &Preparation{Preview: preview, change: change}, nil
}

func (r *Registry) Execute(ctx context.Context, jobID string, scope Scope, call Call, preparation *Preparation) (result Output) {
	started := r.deps.now()
	if err := r.Validate(call); err != nil {
		return Fail(err)
	}
	if ctx.Err() != nil {
		return Fail(ctx.Err())
	}
	if err := r.bindExecutionTarget(ctx, jobID, scope, &call); err != nil {
		return Fail(err)
	}
	if err := r.revalidateExecutionAuthorization(ctx, call); err != nil {
		return Fail(err)
	}
	ctx = withTargetWriteBinding(ctx, call.Target)
	if r.deps.Outcome != nil && call.AuthorizationID != "" && sideEffectKind(call.Name) != "" {
		if call.Name == "terminal_create" {
			// The adjudication→connect→reserve sequence is arbitrated
			// in-process per operation: a concurrent duplicate fails fast
			// with the operation ID instead of establishing a second
			// connection before the ledger can see it.
			release, acquired := r.acquireOperationFlight(OutcomeKey(jobID, call.ID))
			if !acquired {
				failure := Fail(errors.New("相同调用正在执行中，未重复执行"))
				failure.OperationID = operationID(jobID, call.ID, call.Name)
				return failure
			}
			defer release()
		}
		executed := false
		if call.Name == "terminal_create" && r.deps.DurableOutcomes != nil && callTargetsAsset(call) {
			// The durable, owner-scoped ledger adjudicates the operation
			// before any side effect: a conflicted or unfinished family
			// never reconnects blindly, a finished one replays only when
			// its binding and the original session identity still hold,
			// and only a certified-fresh operation begins a new attempt.
			completion, failure := r.adjudicateAndBeginDurableCreate(ctx, jobID, &call)
			if failure != nil {
				return *failure
			}
			defer func() {
				stage := "bind"
				if executed {
					stage = "execute"
				} else if call.ConnectedSessionID != "" {
					stage = "connect"
				}
				completion(&result, stage)
			}()
		}
		if failure := r.bindCallAssetIdentities(ctx, jobID, &call); failure != nil {
			return *failure
		}
		executed = true
		result = r.executeRecorded(ctx, jobID, scope, call, preparation)
	} else {
		result = r.execute(ctx, jobID, scope, call, preparation)
	}
	if r.deps.Audit != nil {
		entry := AuditEntry{SessionID: scope.SessionID, AssetID: scope.AssetID, Kind: "exec", ExitCode: result.ExitCode,
			DurationMS: r.deps.now().Sub(started).Milliseconds(), Payload: map[string]any{"tool": call.Name, "args": redactArguments(call.Args), "run_id": jobID, "call_id": call.ID}}
		_ = r.deps.Audit(context.WithoutCancel(ctx), entry)
	}
	return result
}

func sideEffectKind(name string) outcome.Kind {
	switch name {
	case "exec_commands", "send_keys", "docker_exec", "docker_control":
		return outcome.KindCommand
	case "write_file", "edit_file":
		return outcome.KindFile
	case "db_query":
		return outcome.KindDatabase
	case "terminal_create":
		return outcome.KindCommand
	default:
		return ""
	}
}

func OutcomeKey(jobID, callID string) string {
	return strconv.Itoa(len(jobID)) + ":" + jobID + callID
}

// OutcomeKeyWithIdentity binds the outcome key to the resolved asset
// identities: a retry against the same configuration replays the recorded
// result, while a replaced or reconfigured asset yields a different key and
// executes fresh instead of returning the old asset's result.
func OutcomeKeyWithIdentity(jobID, callID string, identities []AssetIdentity) string {
	if len(identities) == 0 {
		return OutcomeKey(jobID, callID)
	}
	fingerprints := make([]string, 0, len(identities))
	for _, identity := range identities {
		fingerprints = append(fingerprints, identity.AssetID+"@"+identity.ConfigFingerprint)
	}
	sort.Strings(fingerprints)
	return OutcomeKey(jobID, callID) + "|" + strings.Join(fingerprints, ",")
}

// bindCallAssetIdentities resolves and validates the immutable asset
// identities for asset-targeted mutations before any ledger reserve/replay,
// storing them on the call. A configured resolver that fails or returns an
// invalid empty identity set fails the call visibly — an identity-less
// record is never written or replayed; only the path without a configured
// resolver, or one whose target identity genuinely cannot be evidenced
// (ErrAssetIdentityUnverifiable), stays visibly identity-unverifiable. For
// terminal_create with connect=true, an ErrNoLiveSession verdict runs the
// explicit connect flow here so the connected, verified identity is what
// the outcome key binds to.
func (r *Registry) bindCallAssetIdentities(ctx context.Context, jobID string, call *Call) *Output {
	switch call.Name {
	case "terminal_create":
		var args struct {
			AssetID string `json:"asset_id"`
			Connect bool   `json:"connect"`
		}
		if json.Unmarshal(call.Args, &args) != nil || args.AssetID == "" {
			return nil
		}
		return r.bindTerminalCreateIdentity(ctx, jobID, call, args.AssetID, args.Connect)
	case "exec_commands":
		var args ExecCommandsArgs
		if json.Unmarshal(call.Args, &args) != nil {
			return nil
		}
		assetIDs, tags, err := normalizeExecTargetFilters(args.AssetIDs, args.Tags)
		if err != nil {
			failure := Fail(err)
			return &failure
		}
		if len(assetIDs) == 0 && len(tags) == 0 || r.deps.ResolveExecTargetIdentities == nil {
			return nil
		}
		targets, identities, err := r.deps.ResolveExecTargetIdentities(ctx, assetIDs, tags)
		if err != nil {
			if errors.Is(err, ErrAssetIdentityUnverifiable) {
				// A call already bound to identities must never degrade: the
				// bound identity requires an exact match, so an unverifiable
				// target fails closed instead of executing on, recording
				// against, or replaying under the old identity.
				if len(call.AssetIdentities) > 0 {
					failure := Fail(fmt.Errorf("身份解析失败: 目标资产身份已不可验证，与操作绑定身份不一致: %w", ErrAssetIdentityChanged))
					return &failure
				}
				// The batch's identity genuinely cannot be evidenced: it
				// runs unbound and the result marks it unverifiable, the
				// same contract as a registry without the identity resolver.
				return nil
			}
			failure := Fail(fmt.Errorf("身份解析失败: %w", err))
			return &failure
		}
		if err := validateTargetIdentitySet(targets.SessionIDs, identities); err != nil {
			failure := Fail(fmt.Errorf("身份解析失败: %w", err))
			return &failure
		}
		call.AssetIdentities = identities
		return nil
	}
	return nil
}

// bindTerminalCreateIdentity determines the asset identity a terminal_create
// call binds to before the outcome key is computed. With an identity
// resolver configured, a valid resolution binds directly and an
// ErrNoLiveSession verdict with connect=true is explicit connect control
// flow: the session is established and its verified identity bound here. The
// legacy plain-resolver path binds the live session's reported identity when
// the terminal can provide one — so a connect-established binding and a
// later live retry share one key — and stays visibly unverifiable
// otherwise.
func (r *Registry) bindTerminalCreateIdentity(ctx context.Context, jobID string, call *Call, assetID string, connect bool) *Output {
	if r.deps.ResolveAssetSessionIdentity != nil {
		sessionID, identity, err := r.deps.ResolveAssetSessionIdentity(ctx, assetID)
		if err == nil {
			if identity.AssetID == "" || identity.ConfigFingerprint == "" {
				failure := Fail(fmt.Errorf("身份解析失败: 资产 %s 的身份为空: %w", assetID, ErrAssetIdentityChanged))
				return &failure
			}
			call.AssetIdentities = []AssetIdentity{identity}
			call.BoundSessionID = sessionID
			r.state(jobID).rememberAssetOperation(call.ID, call.AssetIdentities, sessionID, "")
			return nil
		}
		if !errors.Is(err, ErrAssetIdentityUnverifiable) {
			if !errors.Is(err, ErrNoLiveSession) || !connect {
				failure := Fail(fmt.Errorf("身份解析失败: %w", err))
				return &failure
			}
			// Before any ConnectAsset, adjudicate the operation's existing
			// outcome records through the stable operation→identity mapping: a
			// finished record replays directly, a non-terminal one fails
			// visibly, and only the certain fresh-execution cases connect —
			// including a durable-certified fresh attempt after a failure,
			// whose idempotency scope keeps the old finished record from
			// short-circuiting the reconnect.
			if output, adjudicated := r.adjudicateRecordedCreate(ctx, jobID, call); adjudicated {
				return output
			}
			connectedID, identity, failure := r.connectAssetSession(ctx, jobID, *call, assetID, err, AssetIdentity{}, false)
			if failure != nil {
				call.ConnectedSessionID = connectedID
				return failure
			}
			call.ConnectedSessionID = connectedID
			call.BoundSessionID = connectedID
			call.AssetIdentities = []AssetIdentity{identity}
			r.state(jobID).rememberAssetOperation(call.ID, call.AssetIdentities, connectedID, connectedID)
			return nil
		}
		// The target's identity genuinely cannot be evidenced (device or
		// inline session): a call already bound to an identity must never
		// degrade — the bound identity requires an exact match, so it fails
		// closed instead of executing on or recording against the
		// unverifiable target. Only an originally unbound call falls through
		// to the plain-resolver path, which keeps it visibly unverifiable.
		if len(call.AssetIdentities) > 0 {
			failure := Fail(fmt.Errorf("身份解析失败: 资产 %s 的身份已不可验证，与操作绑定身份不一致: %w", assetID, ErrAssetIdentityChanged))
			return &failure
		}
	}
	if r.deps.ResolveAssetSession == nil {
		return nil
	}
	sessionID, err := r.deps.ResolveAssetSession(ctx, assetID)
	if err == nil {
		if checker, ok := r.deps.Terminal.(SessionAssetIdentityResolver); ok {
			if identity, cerr := checker.SessionAssetIdentity(ctx, sessionID); cerr == nil && identity.AssetID != "" && identity.ConfigFingerprint != "" {
				call.AssetIdentities = []AssetIdentity{identity}
			}
		}
		call.BoundSessionID = sessionID
		r.state(jobID).rememberAssetOperation(call.ID, call.AssetIdentities, sessionID, "")
		return nil
	}
	if !errors.Is(err, ErrNoLiveSession) || !connect {
		failure := Fail(err)
		return &failure
	}
	// The same adjudication guards the legacy path: a recorded operation
	// never reconnects just to replay or to discover a conflict.
	if output, adjudicated := r.adjudicateRecordedCreate(ctx, jobID, call); adjudicated {
		return output
	}
	connectedID, identity, failure := r.connectAssetSession(ctx, jobID, *call, assetID, err, AssetIdentity{}, false)
	if failure != nil {
		return failure
	}
	call.ConnectedSessionID = connectedID
	call.BoundSessionID = connectedID
	call.AssetIdentities = []AssetIdentity{identity}
	r.state(jobID).rememberAssetOperation(call.ID, call.AssetIdentities, connectedID, connectedID)
	return nil
}

// adjudicateRecordedCreate probes the outcome ledger for existing records of
// this operation before any ConnectAsset, using the stable operation→
// identity mapping recorded when the call first bound: a finished record
// replays directly (naming the originally connected session), a pending or
// running record fails visibly without reconnecting, and a record made under
// a different authorization surfaces the conflict. Only the certain
// fresh-execution cases proceed to connect: no record at all, a record the
// durable ledger has already judged stale, or — when the durable ledger has
// certified this call as a fresh attempt after a failure and set its
// idempotency scope — a binding-matching finished record, which belongs to
// the previous attempt and must not short-circuit the fresh effect.
func (r *Registry) adjudicateRecordedCreate(ctx context.Context, jobID string, call *Call) (*Output, bool) {
	binding, _ := r.state(jobID).assetOperation(call.ID)
	candidates := make([][]AssetIdentity, 0, len(binding.identities)+1)
	candidates = append(candidates, nil)
	for _, identity := range binding.identities {
		candidates = append(candidates, []AssetIdentity{identity})
	}
	for _, identities := range candidates {
		record, err := r.deps.Outcome.Get(ctx, OutcomeKeyWithIdentity(jobID, call.ID, identities))
		if errors.Is(err, outcome.ErrNotFound) {
			continue
		}
		if err != nil {
			failure := Fail(fmt.Errorf("执行记录查询失败: %w", err))
			failure.OperationID = operationID(jobID, call.ID, "terminal_create")
			return &failure, true
		}
		// The same binding contract the ledger enforces on reserve: canonical
		// arguments, Kind, and AuthorizationID all bind the operation, so a
		// same-authorization retry with changed arguments is a conflict, not
		// a replay.
		canonical, canonErr := outcome.CanonicalizeArguments(call.Args)
		if canonErr != nil || record.AuthorizationID != call.AuthorizationID ||
			record.Kind != sideEffectKind(call.Name) ||
			!bytes.Equal(record.CanonicalArguments, canonical) {
			failure := Fail(fmt.Errorf("调用与已记录的执行冲突（授权或参数不一致）: %w", outcome.ErrIdempotenceConflict))
			failure.OperationID = operationID(jobID, call.ID, "terminal_create")
			failure.Text += originalSessionNote(binding)
			failure.Notes += originalSessionNote(binding)
			return &failure, true
		}
		switch record.State {
		case outcome.ExecutionFinished:
			if call.IdempotencyScope != "" {
				// The durable ledger certified this call as a fresh attempt
				// after a failure on the same identity and this caller holds
				// the family claim: the previous attempt's finished record
				// must not replay — the fresh connect proceeds.
				continue
			}
			if r.recordStaleAfterReconfigure(ctx, binding, identities) {
				// The originally bound session now reports a different
				// complete identity: the asset was reconfigured, so this
				// record must not replay — fresh execution proceeds.
				continue
			}
			output := replayOutput(record)
			output.OperationID = operationID(jobID, call.ID, "terminal_create")
			output.Text += originalSessionNote(binding)
			output.Notes += originalSessionNote(binding)
			return &output, true
		case outcome.ExecutionRunning:
			failure := Fail(errors.New("相同调用正在执行中，未重复执行"))
			failure.OperationID = operationID(jobID, call.ID, "terminal_create")
			failure.Text += originalSessionNote(binding)
			failure.Notes += originalSessionNote(binding)
			return &failure, true
		default:
			failure := Fail(fmt.Errorf("相同调用已有未完成的执行记录（state=%s），未重复执行", record.State))
			failure.OperationID = operationID(jobID, call.ID, "terminal_create")
			failure.Text += originalSessionNote(binding)
			failure.Notes += originalSessionNote(binding)
			return &failure, true
		}
	}
	return nil, false
}

// originalSessionNote names the session this operation's explicit connect
// originally established, so replay and conflict verdicts keep the residual
// visible.
func originalSessionNote(binding assetOperationBinding) string {
	if binding.connectedSessionID == "" {
		return ""
	}
	return fmt.Sprintf("；原执行建立的连接 session_id=%s（残留资源保留，未自动清理）", binding.connectedSessionID)
}

// recordStaleAfterReconfigure reports whether the session the operation
// originally bound now reports a different complete identity — the only
// side-effect-free proof that a finished record is stale and fresh execution
// is needed. An unreadable, vanished, or incomplete session proves nothing:
// replay stays the no-side-effect path there.
func (r *Registry) recordStaleAfterReconfigure(ctx context.Context, binding assetOperationBinding, recorded []AssetIdentity) bool {
	if binding.sessionID == "" {
		return false
	}
	checker, ok := r.deps.Terminal.(SessionAssetIdentityResolver)
	if !ok {
		return false
	}
	current, err := checker.SessionAssetIdentity(ctx, binding.sessionID)
	if err != nil || current.AssetID == "" || current.ConfigFingerprint == "" {
		return false
	}
	reference := recorded
	if len(reference) == 0 {
		reference = binding.identities
	}
	if len(reference) == 0 {
		return false
	}
	for _, identity := range reference {
		if current == identity {
			return false
		}
	}
	return true
}

// durableOutcomeWriteTimeout bounds the durable ledger's progress and
// completion writes so a canceled or hung caller context cannot strand the
// residual write or the final record.
const durableOutcomeWriteTimeout = 5 * time.Second

// callTargetsAsset reports whether the call targets an explicit asset.
func callTargetsAsset(call Call) bool {
	var args struct {
		AssetID string `json:"asset_id"`
	}
	return json.Unmarshal(call.Args, &args) == nil && args.AssetID != ""
}

// durableCreateBinding ties a durable outcome record to the exact call that
// produced it: canonical arguments, the authorization that approved it, and
// the operation kind — the same binding contract the idempotency ledger
// enforces on reserve.
type durableCreateBinding struct {
	CanonicalArgs   string `json:"canonical_args"`
	AuthorizationID string `json:"authorization_id"`
	Kind            string `json:"kind"`
}

// durableCreateOutcome is the recorded payload of one terminal_create
// attempt: its binding, the identities it executed under, the session they
// were bound from, the session this call connected, and the created tab on
// success.
type durableCreateOutcome struct {
	Binding            durableCreateBinding `json:"binding"`
	Identities         []AssetIdentity      `json:"identities,omitempty"`
	SessionID          string               `json:"session_id,omitempty"`
	ConnectedSessionID string               `json:"connected_session_id,omitempty"`
	Output             *durableCreateOutput `json:"output,omitempty"`
}

type durableCreateOutput struct {
	TabID     string `json:"tab_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Title     string `json:"title,omitempty"`
	Handle    string `json:"handle,omitempty"`
}

// adjudicateAndBeginDurableCreate consults the durable, owner-scoped outcome
// ledger before any side effect: a conflicted or unfinished family fails
// visibly without reconnecting, a finished attempt replays only when its
// binding and the original session identity still hold, and only a
// certified-fresh operation claims the family for a new attempt. The claim
// is atomic at the durable layer, so concurrent callers across processes
// cannot both be admitted before connecting. The returned completion writes
// the attempt's final record and releases the claim.
func (r *Registry) adjudicateAndBeginDurableCreate(ctx context.Context, jobID string, call *Call) (func(*Output, string), *Output) {
	operation := operationID(jobID, call.ID, "terminal_create")
	adjudication, err := r.deps.DurableOutcomes.AdjudicateOutcome(ctx, operation)
	if err != nil {
		failure := Fail(fmt.Errorf("执行记录裁定查询失败: %w", err))
		return nil, &failure
	}
	freshAfterFailure := false
	switch {
	case adjudication.Conflict:
		failure := Fail(errors.New("相同调用存在冲突的执行记录，未重复执行"))
		failure.OperationID = operation
		return nil, &failure
	case adjudication.Pending != nil:
		failure := Fail(fmt.Errorf("相同调用已有未完成的执行记录（state=%s），未重复执行", adjudication.Pending.State))
		failure.OperationID = operation
		if adjudication.Pending.ResidualSessionID != "" {
			note := fmt.Sprintf("；残留会话 session_id=%s（未自动清理）", adjudication.Pending.ResidualSessionID)
			failure.Text += note
			failure.Notes += note
		}
		return nil, &failure
	case adjudication.Terminal != nil:
		if output, done := r.replayDurableCreate(ctx, jobID, call, adjudication.Terminal); done {
			return nil, output
		}
		// The record is stale (the asset was reconfigured) or finally
		// failed: a fresh attempt proceeds.
		freshAfterFailure = adjudication.Terminal.State == "failed"
	}
	claimID, claimed, err := r.deps.DurableOutcomes.ClaimOutcome(ctx, operation, "terminal_create")
	if err != nil {
		failure := Fail(fmt.Errorf("执行记录登记失败: %w", err))
		return nil, &failure
	}
	if !claimed {
		// A concurrent attempt claimed the family after this caller's
		// adjudication read: adjudicate once more so the verdict reflects
		// the winner's record, and never connect.
		adjudication, err := r.deps.DurableOutcomes.AdjudicateOutcome(ctx, operation)
		if err != nil {
			failure := Fail(fmt.Errorf("执行记录裁定查询失败: %w", err))
			return nil, &failure
		}
		failure := Fail(errors.New("相同调用已有未完成的执行记录（state=initiated），未重复执行"))
		if adjudication.Pending != nil && adjudication.Pending.ResidualSessionID != "" {
			failure.Text += fmt.Sprintf("；残留会话 session_id=%s（未自动清理）", adjudication.Pending.ResidualSessionID)
			failure.Notes += fmt.Sprintf("；残留会话 session_id=%s（未自动清理）", adjudication.Pending.ResidualSessionID)
		}
		failure.OperationID = operation
		return nil, &failure
	}
	// The claim is held, so the family is frozen against other attempts —
	// but the first adjudication may have raced with an attempt that
	// completed and released between that read and this claim. Re-adjudicate
	// before any side effect: a terminal record that landed in the window
	// replays (or conflicts) instead of executing a duplicate, and anything
	// other than this caller's own in-flight row aborts safely.
	adjudication, err = r.deps.DurableOutcomes.AdjudicateOutcome(ctx, operation)
	if err != nil {
		failure := Fail(fmt.Errorf("执行记录裁定查询失败: %w", err))
		r.abortDurableClaim(ctx, operation, &failure)
		return nil, &failure
	}
	switch {
	case adjudication.Conflict:
		failure := Fail(errors.New("相同调用存在冲突的执行记录，未重复执行"))
		failure.OperationID = operation
		r.abortDurableClaim(ctx, operation, &failure)
		return nil, &failure
	case adjudication.Pending != nil && adjudication.Pending.OperationID != claimID:
		failure := Fail(errors.New("相同调用已有未完成的执行记录（state=initiated），未重复执行"))
		failure.OperationID = operation
		if adjudication.Pending.ResidualSessionID != "" {
			failure.Text += fmt.Sprintf("；残留会话 session_id=%s（未自动清理）", adjudication.Pending.ResidualSessionID)
			failure.Notes += fmt.Sprintf("；残留会话 session_id=%s（未自动清理）", adjudication.Pending.ResidualSessionID)
		}
		r.abortDurableClaim(ctx, operation, &failure)
		return nil, &failure
	case adjudication.Terminal != nil:
		if output, done := r.replayDurableCreate(ctx, jobID, call, adjudication.Terminal); done {
			r.abortDurableClaim(ctx, operation, output)
			return nil, output
		}
		// The record is stale (the asset was reconfigured) or finally
		// failed: this caller's fresh attempt proceeds.
		freshAfterFailure = freshAfterFailure || adjudication.Terminal.State == "failed"
	}
	attempt := operation + ":" + newOutcomeAttemptNonce()
	if freshAfterFailure {
		// The fresh attempt follows a failed record on the same identity:
		// scope the inner idempotency key to this attempt so the old
		// finished failure cannot short-circuit the effect.
		call.IdempotencyScope = attempt
	}
	return func(result *Output, stage string) {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), durableOutcomeWriteTimeout)
		defer cancel()
		r.completeDurableCreate(writeCtx, operation, attempt, call, result, stage)
	}, nil
}

// abortDurableClaim releases the family claim on an abort path; a release
// failure is appended to the result instead of hidden.
func (r *Registry) abortDurableClaim(ctx context.Context, family string, result *Output) {
	if err := r.deps.DurableOutcomes.ReleaseOutcome(ctx, family); err != nil {
		note := fmt.Sprintf("；durable 执行记录释放失败: %v", err)
		result.Text += note
		result.Notes += note
	}
}

// replayDurableCreate returns the recorded result of a finished attempt when
// the durable binding still matches this call and the originally bound
// session still reports the same complete identity. A binding mismatch is a
// conflict; a reconfigured asset falls through to fresh execution.
func (r *Registry) replayDurableCreate(ctx context.Context, jobID string, call *Call, record *OutcomeAttempt) (*Output, bool) {
	operation := operationID(jobID, call.ID, "terminal_create")
	var recorded durableCreateOutcome
	canonical, canonErr := outcome.CanonicalizeArguments(call.Args)
	if err := json.Unmarshal(record.Result, &recorded); canonErr != nil || err != nil ||
		recorded.Binding.AuthorizationID != call.AuthorizationID ||
		recorded.Binding.Kind != string(sideEffectKind(call.Name)) ||
		recorded.Binding.CanonicalArgs != string(canonical) {
		failure := Fail(fmt.Errorf("调用与已记录的执行冲突（授权或参数不一致）: %w", outcome.ErrIdempotenceConflict))
		failure.OperationID = operation
		return &failure, true
	}
	// A record bound to a specific identity must never replay when the
	// current target can no longer evidence one: the replay cannot certify
	// the binding still holds, so it fails closed instead of returning the
	// old result for an unverifiable target. A vacated session (no live
	// session at all) is not this verdict — replay stays the no-side-effect
	// path there.
	if len(recorded.Identities) > 0 && r.deps.ResolveAssetSessionIdentity != nil {
		if _, _, err := r.deps.ResolveAssetSessionIdentity(ctx, recorded.Identities[0].AssetID); errors.Is(err, ErrAssetIdentityUnverifiable) {
			failure := Fail(fmt.Errorf("资产 %s 的身份已不可验证，与已记录执行的绑定身份不一致: %w", recorded.Identities[0].AssetID, ErrAssetIdentityChanged))
			failure.OperationID = operation
			return &failure, true
		}
	}
	if record.State != "succeeded" || r.durableCreateStale(ctx, recorded) {
		return nil, false
	}
	output := Output{OK: true, Text: fmt.Sprintf("调用已执行过，返回已记录的结果（outcome=%s）", record.State), OperationID: operation}
	if recorded.ConnectedSessionID != "" {
		note := fmt.Sprintf("；原执行建立的连接 session_id=%s（残留资源保留，未自动清理）", recorded.ConnectedSessionID)
		output.Text += note
		output.Notes += note
	}
	if recorded.Output != nil {
		output.Terminal = &CreatedTerminal{TabID: recorded.Output.TabID, SessionID: recorded.Output.SessionID, Title: recorded.Output.Title, Handle: recorded.Output.Handle}
	}
	return &output, true
}

// durableCreateStale reports whether the asset's present identity differs
// from the recorded one — the only side-effect-free proof that the recorded
// result is stale and fresh execution is needed. The current resolution is
// authoritative: a different valid identity from the resolver proves the
// asset was reconfigured or replaced even when the originally bound session
// still lives and still reports the old identity. When the current
// resolution is unavailable (no live session or unreadable), the originally
// bound session's own report is the fallback signal; an unreadable or
// vanished session proves nothing and replay stays the no-side-effect path.
func (r *Registry) durableCreateStale(ctx context.Context, recorded durableCreateOutcome) bool {
	if len(recorded.Identities) == 0 {
		return false
	}
	recordedIdentity := recorded.Identities[0]
	if r.deps.ResolveAssetSessionIdentity != nil {
		if _, current, err := r.deps.ResolveAssetSessionIdentity(ctx, recordedIdentity.AssetID); err == nil && current.AssetID != "" && current.ConfigFingerprint != "" {
			return current != recordedIdentity
		}
	} else if r.deps.ResolveAssetSession != nil {
		// The legacy plain resolver plus the resolved session's own report is
		// the fallback current-identity source.
		if sessionID, err := r.deps.ResolveAssetSession(ctx, recordedIdentity.AssetID); err == nil {
			if checker, ok := r.deps.Terminal.(SessionAssetIdentityResolver); ok {
				if current, cerr := checker.SessionAssetIdentity(ctx, sessionID); cerr == nil && current.AssetID != "" && current.ConfigFingerprint != "" {
					return current != recordedIdentity
				}
			}
		}
	}
	if recorded.SessionID != "" {
		if checker, ok := r.deps.Terminal.(SessionAssetIdentityResolver); ok {
			if current, err := checker.SessionAssetIdentity(ctx, recorded.SessionID); err == nil && current.AssetID != "" && current.ConfigFingerprint != "" && current != recordedIdentity {
				return true
			}
		}
	}
	return false
}

// completeDurableCreate writes the attempt's final record: the binding, the
// identities and sessions it ran under, and the created tab on success or
// the failure stage and residual on failure. A record write failure is
// surfaced on the result instead of hidden.
func (r *Registry) completeDurableCreate(ctx context.Context, family, attemptID string, call *Call, result *Output, stage string) {
	recorded := durableCreateOutcome{
		Identities:         call.AssetIdentities,
		SessionID:          call.BoundSessionID,
		ConnectedSessionID: call.ConnectedSessionID,
	}
	recorded.Binding.AuthorizationID = call.AuthorizationID
	recorded.Binding.Kind = string(sideEffectKind(call.Name))
	if canonical, err := outcome.CanonicalizeArguments(call.Args); err == nil {
		recorded.Binding.CanonicalArgs = string(canonical)
	}
	attempt := OutcomeAttempt{
		OperationID:       attemptID,
		Kind:              "terminal_create",
		ResidualSessionID: call.ConnectedSessionID,
	}
	if attempt.ResidualSessionID == "" {
		// The execution-time reconnect path cannot write the call's
		// residual field (the tool holds a copy), but the claim row already
		// carries it from the progress write — recover it from there.
		if adjudication, err := r.deps.DurableOutcomes.AdjudicateOutcome(ctx, family); err == nil && adjudication.Pending != nil {
			attempt.ResidualSessionID = adjudication.Pending.ResidualSessionID
			recorded.ConnectedSessionID = attempt.ResidualSessionID
		}
	}
	if result.OK {
		attempt.State = "succeeded"
		created := result.Terminal
		if created == nil {
			created = result.CreatedTab
		}
		if created != nil {
			recorded.Output = &durableCreateOutput{TabID: created.TabID, SessionID: created.SessionID, Title: created.Title, Handle: created.Handle}
		}
	} else {
		attempt.State = "failed"
		attempt.FailureStage = stage
		attempt.Error = result.Text
	}
	if encoded, err := json.Marshal(recorded); err == nil {
		attempt.Result = encoded
	}
	if err := r.deps.DurableOutcomes.CompleteOutcome(ctx, family, attempt); err != nil {
		note := fmt.Sprintf("；durable 执行记录写入失败: %v", err)
		result.Text += note
		result.Notes += note
	}
}

// newOutcomeAttemptNonce returns a per-attempt suffix for the durable
// operation record: the nanosecond prefix keeps the attempt history in
// attempt order under the store's newest-first prefix scan, and the random
// suffix keeps it unique across processes.
func newOutcomeAttemptNonce() string {
	var bytes [4]byte
	_, _ = rand.Read(bytes[:])
	return fmt.Sprintf("%016x-%s", time.Now().UnixNano(), hex.EncodeToString(bytes[:]))
}

func GuardAuthorizationID(decision guard.Decision) string {
	encoded, err := json.Marshal(decision)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return "guard:" + hex.EncodeToString(sum[:])
}

func (r *Registry) executeRecorded(ctx context.Context, jobID string, scope Scope, call Call, preparation *Preparation) Output {
	key := OutcomeKeyWithIdentity(jobID, call.ID, call.AssetIdentities)
	if call.IdempotencyScope != "" {
		// A durable-adjudicated fresh attempt scopes its own key: the old
		// finished failure under the family key must not short-circuit it.
		key = OutcomeKeyWithIdentity(jobID, call.IdempotencyScope, call.AssetIdentities)
	}
	request := outcome.Request{
		IdempotenceKey:  key,
		AuthorizationID: outcomeAuthorizationID(call),
		Kind:            sideEffectKind(call.Name),
		Arguments:       call.Args,
	}
	var (
		ranEffect bool
		branch    Output
	)
	record, err := r.deps.Outcome.Execute(ctx, request, func(effectCtx context.Context) (outcome.Completion, error) {
		ranEffect = true
		branch = r.execute(effectCtx, jobID, scope, call, preparation)
		return completionFromOutput(branch)
	})
	// The deterministic operation ID of this call: replays and every
	// effect-initiated outcome reference the same operation.
	operation := operationID(jobID, call.ID, call.Name)
	switch {
	case err == nil && !ranEffect:
		output := replayOutput(record)
		output.OperationID = operation
		return withConnectedResidual(call, operation, output)
	case err == nil:
		return branch
	case errors.Is(err, outcome.ErrInProgress):
		return withConnectedResidual(call, operation, Fail(fmt.Errorf("相同调用正在执行中，未重复执行: %w", err)))
	case errors.Is(err, outcome.ErrIdempotenceConflict):
		return withConnectedResidual(call, operation, Fail(fmt.Errorf("调用与已记录的执行冲突（授权、参数或执行目标不一致）: %w", err)))
	case !ranEffect:
		return withConnectedResidual(call, operation, Fail(fmt.Errorf("执行结果记录失败: %w", err)))
	case record.State != outcome.ExecutionFinished || record.Audit.State == outcome.AuditPending:

		result := Fail(fmt.Errorf("操作已执行，但结果记录失败：%w", err))
		result.OperationID = branch.OperationID
		if result.OperationID == "" {
			result.OperationID = operation
		}
		return withConnectedResidual(call, operation, result)
	case record.Audit.State == outcome.AuditFailed:
		if branch.ModelText == "" {
			branch.ModelText = provider.ProjectModelOutput(call.Name, branch.OK, branch.Text, branch.Truncated, branch.Panic)
		}
		branch.Text += "\n[结果审计写入失败: " + record.Audit.Error + "]"
		branch.ModelText += "\n[audit write failed: " + provider.ProjectModelErrorText(record.Audit.Error) + "]"
		return branch
	default:
		return branch
	}
}

// withConnectedResidual keeps the session this call's pre-key connect
// established visible on replay, conflict, in-progress, and record-failure
// paths: the session exists because of this call, so every one of those
// outcomes names it as a residual together with the operation ID.
func withConnectedResidual(call Call, operation string, output Output) Output {
	if call.ConnectedSessionID == "" {
		return output
	}
	if output.OperationID == "" {
		output.OperationID = operation
	}
	note := fmt.Sprintf("；已建立的会话 session_id=%s 作为残留资源保留（未自动清理）", call.ConnectedSessionID)
	output.Text += note
	output.Notes += note
	return output
}

func completionFromOutput(output Output) (outcome.Completion, error) {
	if !output.OK {
		return outcome.Completion{}, errors.New(output.Text)
	}
	if output.Truncated || output.ExecFailed {
		return outcome.Completion{}, errors.New("工具输出被截断或传输失败，无法确定执行结果")
	}
	if output.ExitUnknown {
		return outcome.Completion{Outcome: outcome.OutcomeUnknown}, nil
	}
	code := output.ExitCode
	if code != 0 {
		return outcome.Completion{Outcome: outcome.OutcomeFailed, ExitCode: &code}, nil
	}
	return outcome.Completion{Outcome: outcome.OutcomeAccepted, ExitCode: &code}, nil
}

func replayOutput(record outcome.Record) Output {
	text := fmt.Sprintf("调用已执行过，返回已记录的结果（outcome=%s）", record.Outcome)
	switch record.Outcome {
	case outcome.OutcomeAccepted:
		return Output{OK: true, Text: text}
	case outcome.OutcomeUnknown:
		// An error-derived unknown stays an explicit failure; a clean
		// unknown (e.g. a waited-on command with no reported exit code)
		// replays as structured exit-unknown, never as exit 0.
		if record.Result.Error != "" {
			return Output{Text: text + ": " + record.Result.Error, ExitCode: 1}
		}
		return Output{OK: true, Text: text, ExitUnknown: true}
	case outcome.OutcomeFailed:
		code := 1
		if record.Result.ExitCode != nil {
			code = *record.Result.ExitCode
		}
		return Output{OK: true, Text: text, ExitCode: code}
	default:
		if record.Result.Error != "" {
			text += ": " + record.Result.Error
		}
		return Output{Text: text, ExitCode: 1}
	}
}

func (r *Registry) execute(ctx context.Context, jobID string, scope Scope, call Call, preparation *Preparation) Output {
	output, err := Guarded(ctx, call.Name, func() (Output, error) {
		return r.executeTool(ctx, jobID, scope, call, preparation), nil
	})
	if err != nil {
		return Fail(err)
	}
	return output
}

func (r *Registry) executeTool(ctx context.Context, jobID string, scope Scope, call Call, preparation *Preparation) Output {
	switch call.Name {
	case "exec_commands":
		return r.execCommands(ctx, jobID, scope, call)
	case "read_file":
		return r.readFile(ctx, jobID, scope, call.Args)
	case "write_file", "edit_file":
		return r.writePrepared(ctx, jobID, scope, call, preparation)
	case "list_dir":
		return r.listDir(ctx, scope, call.Args)
	case "search_files":
		return r.searchFiles(ctx, scope, call.Args)
	case "read_screen":
		return r.readScreen(ctx, jobID, scope, call.Args)
	case "send_keys":
		return r.sendKeys(ctx, jobID, scope, call)
	case "terminal_create":
		return r.terminalCreate(ctx, jobID, scope, call)
	case "terminal_rename":
		return r.terminalRename(ctx, jobID, scope, call.Args)
	case "terminal_list":
		return r.terminalList(ctx, scope, call.Args)
	case "operation_result":
		return r.operationResult(ctx, scope, call.Args)
	case "wait_for":
		return r.waitFor(ctx, jobID, scope, call.Args, 120)
	case "shell_history":
		return r.shellHistory(ctx, scope, call.Args)
	case "docker_ps":
		return r.dockerPS(ctx, scope)
	case "docker_logs":
		return r.dockerLogs(ctx, scope, call.Args)
	case "docker_exec":
		return r.dockerExec(ctx, scope, call.Args)
	case "docker_control":
		return r.dockerControl(ctx, scope, call.Args)
	case "db_list_tables":
		return r.dbListTables(ctx, scope, call.Args)
	case "db_describe":
		return r.dbDescribe(ctx, scope, call.Args)
	case "db_query":
		return r.dbQuery(ctx, scope, call.Args)
	case "redis_scan":
		return r.redisScan(ctx, scope, call.Args)
	case "list_assets":
		return r.listAssets(ctx)
	case "ask_user":
		return askUser(call.Args)
	case "todo_write":
		return r.todoWrite(jobID, call.Args)
	case "exit_plan_mode":
		return r.exitPlanMode(jobID, call.Args)
	case NotifyTool:
		return r.notify(ctx, call.Args)
	case "load_skill":
		if !skillLoaderConfigured(r.deps.Skills) {
			return Fail(fmt.Errorf("未知工具 %s", call.Name))
		}
		return r.deps.Skills.LoadSkill(ctx, call.Args)
	default:
		return Fail(fmt.Errorf("未知工具 %s", call.Name))
	}
}

func decode(args json.RawMessage, target any) error {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return invalid("%v", err)
	}
	return nil
}

func toolSchemas() []Schema {
	return buildSchemas()
}
