package sync

import (
	"context"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// optInID names one per-user sync opt-in switch. optInNone marks kinds that always sync.
type optInID int

const (
	optInNone optInID = iota
	optInKnownHost
	optInAIProfile
	optInAIPermission
	optInAIGrant
	optInPreferences
	optInStateRule
	optInRunbookTemplate
	optInSkill
	optInMemory
	optInCron
	optInCount
)

// optInSpec binds an opt-in switch to its per-user storage and its fields in the IPC view.
type optInSpec struct {
	id    optInID
	get   func(*store.Store, context.Context, string) (bool, error)
	set   func(*store.Store, context.Context, string, bool) error
	view  func(*KindOptIn) *bool
	patch func(KindOptInPatch) *bool
}

// optInSpecs is ordered: the order defines load/set order, the cursor opt-in key, and the compare token,
// so new switches must be appended.
var optInSpecs = []optInSpec{
	{
		id: optInKnownHost, get: (*store.Store).KnownHostSyncOptIn, set: (*store.Store).SetKnownHostSyncOptIn,
		view: func(v *KindOptIn) *bool { return &v.KnownHost }, patch: func(p KindOptInPatch) *bool { return p.KnownHost },
	},
	{
		id: optInAIProfile, get: (*store.Store).AIProfileSyncOptIn, set: (*store.Store).SetAIProfileSyncOptIn,
		view: func(v *KindOptIn) *bool { return &v.AIProfile }, patch: func(p KindOptInPatch) *bool { return p.AIProfile },
	},
	{
		id: optInAIPermission, get: (*store.Store).AIPermissionSyncOptIn, set: (*store.Store).SetAIPermissionSyncOptIn,
		view: func(v *KindOptIn) *bool { return &v.AIPermission }, patch: func(p KindOptInPatch) *bool { return p.AIPermission },
	},
	{
		id: optInAIGrant, get: (*store.Store).AIGrantSyncOptIn, set: (*store.Store).SetAIGrantSyncOptIn,
		view: func(v *KindOptIn) *bool { return &v.AIGrant }, patch: func(p KindOptInPatch) *bool { return p.AIGrant },
	},
	{
		id: optInPreferences, get: (*store.Store).PreferencesSyncOptIn, set: (*store.Store).SetPreferencesSyncOptIn,
		view: func(v *KindOptIn) *bool { return &v.Preferences }, patch: func(p KindOptInPatch) *bool { return p.Preferences },
	},
	{
		id: optInStateRule, get: (*store.Store).StateRuleSyncOptIn, set: (*store.Store).SetStateRuleSyncOptIn,
		view: func(v *KindOptIn) *bool { return &v.StateRules }, patch: func(p KindOptInPatch) *bool { return p.StateRules },
	},
	{
		id: optInRunbookTemplate, get: (*store.Store).RunbookTemplateSyncOptIn, set: (*store.Store).SetRunbookTemplateSyncOptIn,
		view: func(v *KindOptIn) *bool { return &v.RunbookTemplates }, patch: func(p KindOptInPatch) *bool { return p.RunbookTemplates },
	},
	{
		id: optInSkill, get: (*store.Store).SkillSyncOptIn, set: (*store.Store).SetSkillSyncOptIn,
		view: func(v *KindOptIn) *bool { return &v.Skills }, patch: func(p KindOptInPatch) *bool { return p.Skills },
	},
	{
		id: optInMemory, get: (*store.Store).MemorySyncOptIn, set: (*store.Store).SetMemorySyncOptIn,
		view: func(v *KindOptIn) *bool { return &v.Memory }, patch: func(p KindOptInPatch) *bool { return p.Memory },
	},
	{
		id: optInCron, get: (*store.Store).CronSyncOptIn, set: (*store.Store).SetCronSyncOptIn,
		view: func(v *KindOptIn) *bool { return &v.Cron }, patch: func(p KindOptInPatch) *bool { return p.Cron },
	},
}

// revisionSource selects the payload field that carries an object's LWW revision.
type revisionSource int

const (
	revisionUpdated revisionSource = iota // updatedAt, or a later deletedAt
	revisionAdded                         // addedAt
	revisionEnded                         // endedAt
	revisionDeleted                       // deletedAt only
)

// localCollection is the shared state for one collectLocalObjects pass.
type localCollection struct {
	objects map[string]localObject
	report  *SyncReport
	optIn   kindOptIn
	userID  string
}

// kindSpec describes everything kind-specific about one sync object kind in the Go engine. Adding a kind means
// adding one spec here; browser mode collects through the per-kind IPC in collect.go (or, with browserCollect,
// the generic CommandCollectKindObjects) and the frontend push, so a new kind also needs frontend support.
type kindSpec struct {
	kind string
	// rank orders kinds by dependency for push and plaintext apply.
	rank int
	// optIn gates collection and remote apply of the kind and of tombstones targeting it.
	optIn optInID
	// aiOwner kinds (and tombstones targeting them) require an AI owner identity before a batch writes.
	aiOwner bool
	// strictID requires a store ID; otherwise any valid sync object ID is accepted.
	strictID bool
	// idInPayload requires the decoded payload's ID to equal the object ID.
	idInPayload bool
	revision    revisionSource
	// newPayload returns the strict decode target for a plaintext payload.
	newPayload func(objectID string) (any, error)
	// displayName names the object in compare results; "" falls back to the object ID. nil uses the payload name.
	displayName func(objectID string, plaintext []byte) string
	// validateApply returns a warning that rejects a browser-applied canonical payload before it writes.
	validateApply func(e *Engine, ctx context.Context, canonical []byte) string
	apply         func(e *Engine, ctx context.Context, objectID string, plaintext []byte, report *SyncReport, userID string) (bool, bool)
	// applyTombstone is nil when the kind cannot be a tombstone target.
	applyTombstone func(e *Engine, ctx context.Context, objectID string, tombstone tombstoneObject, report *SyncReport, userID string) (bool, bool)
	// collect adds the kind's local objects; specs run in registry order, and later specs win on ID collisions.
	// Tombstones have no collect hook: collectLocalObjects always collects them after every other kind.
	collect func(e *Engine, ctx context.Context, c *localCollection) error
	// collectNeedsUser skips collection without a session user.
	collectNeedsUser bool
	// browserCollect lets the browser collect the kind's payloads through CommandCollectKindObjects
	// instead of a kind-specific collect command.
	browserCollect bool
}

// syncKinds is the kind registry in collection order. It is assigned in init because spec functions refer back to the registry.
var (
	syncKinds   []kindSpec
	syncKindMap map[string]*kindSpec
	// objectKinds is the validated decryption attempt order, copied from decryptOrder.
	objectKinds []string
	// aiOwnerKinds lists the kinds whose objects and tombstones require an AI owner identity.
	aiOwnerKinds []string
	// unknownKindRank orders kinds this build does not know after every registered kind.
	unknownKindRank int
)

// decryptOrder is the fixed decryption attempt order; AAD binds kind, so a wrong kind can never decrypt.
// It must list every registered kind exactly once.
var decryptOrder = []string{KindGroup, KindAsset, KindCredential, KindSnippet, KindTombstone, KindTranscript, KindKnownHost, KindAIProfile, KindAIPermission, KindAIGrant, KindPreference,
	KindStateRule, KindRunbookTemplate, KindSkill, KindMemory, KindCron}

func init() {
	syncKinds = []kindSpec{
		{
			kind: KindGroup, rank: 0, strictID: true, idInPayload: true,
			newPayload:    newPayload[groupObject],
			validateApply: (*Engine).validateGroupApply,
			apply: func(e *Engine, ctx context.Context, _ string, plaintext []byte, report *SyncReport, _ string) (bool, bool) {
				return e.applyGroupObject(ctx, plaintext, report)
			},
			applyTombstone: func(e *Engine, ctx context.Context, objectID string, tombstone tombstoneObject, report *SyncReport, _ string) (bool, bool) {
				return e.applyGroupTombstone(ctx, objectID, tombstone.DeletedAt, report)
			},
			collect: (*Engine).collectGroupObjects,
		},
		{
			kind: KindCredential, rank: 1, strictID: true, idInPayload: true,
			newPayload: newPayload[credentialObject],
			apply: func(e *Engine, ctx context.Context, _ string, plaintext []byte, report *SyncReport, _ string) (bool, bool) {
				return e.applyCredentialObject(ctx, plaintext, report)
			},
			applyTombstone: func(e *Engine, ctx context.Context, objectID string, tombstone tombstoneObject, report *SyncReport, _ string) (bool, bool) {
				return e.applyCredentialTombstone(ctx, objectID, tombstone.DeletedAt, report)
			},
			collect: (*Engine).collectCredentialObjects,
		},
		{
			kind: KindSnippet, rank: 2, strictID: true, idInPayload: true,
			newPayload: newPayload[snippetObject],
			apply: func(e *Engine, ctx context.Context, _ string, plaintext []byte, report *SyncReport, _ string) (bool, bool) {
				return e.applySnippetObject(ctx, plaintext, report)
			},
			applyTombstone: func(e *Engine, ctx context.Context, objectID string, tombstone tombstoneObject, report *SyncReport, _ string) (bool, bool) {
				return e.applySnippetTombstone(ctx, objectID, tombstone.DeletedAt, report)
			},
			collect: (*Engine).collectSnippetObjects,
		},
		{
			kind: KindAsset, rank: 3, strictID: true, idInPayload: true,
			newPayload: newPayload[assetObject],
			apply: func(e *Engine, ctx context.Context, _ string, plaintext []byte, report *SyncReport, _ string) (bool, bool) {
				return e.applyAssetObject(ctx, plaintext, report)
			},
			collect: (*Engine).collectAssetObjects,
		},
		{
			kind: KindTranscript, rank: 15, strictID: true, idInPayload: true, revision: revisionEnded,
			newPayload:  newPayload[transcriptObject],
			displayName: transcriptDisplayName,
			apply: func(e *Engine, ctx context.Context, _ string, plaintext []byte, report *SyncReport, _ string) (bool, bool) {
				return e.applyTranscriptObject(ctx, plaintext, report)
			},
			applyTombstone: func(e *Engine, ctx context.Context, objectID string, tombstone tombstoneObject, report *SyncReport, _ string) (bool, bool) {
				return e.applyTranscriptTombstone(ctx, objectID, tombstone.DeletedAt, report)
			},
			collect: (*Engine).collectTranscriptObjects,
		},
		{
			kind: KindKnownHost, rank: 4, optIn: optInKnownHost, strictID: true, idInPayload: true, revision: revisionAdded,
			newPayload:  newPayload[knownHostObject],
			displayName: knownHostDisplayName,
			apply: func(e *Engine, ctx context.Context, _ string, plaintext []byte, report *SyncReport, _ string) (bool, bool) {
				return e.applyKnownHostObject(ctx, plaintext, report)
			},
			applyTombstone: func(e *Engine, ctx context.Context, objectID string, tombstone tombstoneObject, report *SyncReport, _ string) (bool, bool) {
				return e.applyKnownHostTombstone(ctx, objectID, tombstone, report)
			},
			collect: (*Engine).collectKnownHostObjects,
		},
		{
			kind: KindAIProfile, rank: 5, optIn: optInAIProfile, aiOwner: true, strictID: true, idInPayload: true,
			newPayload: newPayload[aiProfileObject],
			apply: func(e *Engine, ctx context.Context, _ string, plaintext []byte, report *SyncReport, _ string) (bool, bool) {
				return e.applyAIProfileObject(ctx, plaintext, report)
			},
			applyTombstone: func(e *Engine, ctx context.Context, objectID string, tombstone tombstoneObject, report *SyncReport, _ string) (bool, bool) {
				return e.applyAIProfileTombstone(ctx, objectID, tombstone.DeletedAt, report)
			},
			collect: (*Engine).collectAIProfileObjects,
		},
		{
			kind: KindAIPermission, rank: 6, optIn: optInAIPermission, aiOwner: true, idInPayload: true,
			newPayload:  newPayload[aiPermissionObject],
			displayName: func(string, []byte) string { return "全局 AI 权限" },
			apply: func(e *Engine, ctx context.Context, _ string, plaintext []byte, report *SyncReport, _ string) (bool, bool) {
				return e.applyAIPermissionObject(ctx, plaintext, report)
			},
			applyTombstone: func(e *Engine, ctx context.Context, objectID string, tombstone tombstoneObject, report *SyncReport, _ string) (bool, bool) {
				return e.applyAIPermissionTombstone(ctx, objectID, tombstone.DeletedAt, report)
			},
			collect: func(e *Engine, ctx context.Context, c *localCollection) error {
				return e.collectAIPermission(ctx, c.objects)
			},
			collectNeedsUser: true,
		},
		{
			kind: KindAIGrant, rank: 7, optIn: optInAIGrant, aiOwner: true, idInPayload: true,
			newPayload:  newAIGrantPayload,
			displayName: aiGrantDisplayName,
			apply: func(e *Engine, ctx context.Context, objectID string, plaintext []byte, report *SyncReport, _ string) (bool, bool) {
				return e.applyAIGrantObject(ctx, objectID, plaintext, report)
			},
			applyTombstone: func(e *Engine, ctx context.Context, objectID string, tombstone tombstoneObject, report *SyncReport, _ string) (bool, bool) {
				return e.applyAIGrantTombstone(ctx, objectID, tombstone.DeletedAt, report)
			},
			collect: func(e *Engine, ctx context.Context, c *localCollection) error {
				return e.collectAIGrantObjects(ctx, c.objects, c.report)
			},
			collectNeedsUser: true,
		},
		{
			kind: KindPreference, rank: 8, optIn: optInPreferences, idInPayload: true,
			newPayload:  newPayload[preferenceObject],
			displayName: preferenceDisplayName,
			apply: func(e *Engine, ctx context.Context, _ string, plaintext []byte, report *SyncReport, userID string) (bool, bool) {
				return e.applyPreferenceObject(ctx, plaintext, report, userID)
			},
			applyTombstone: func(e *Engine, ctx context.Context, objectID string, tombstone tombstoneObject, report *SyncReport, userID string) (bool, bool) {
				return e.applyPreferenceTombstone(ctx, objectID, tombstone.DeletedAt, report, userID)
			},
			collect: func(e *Engine, ctx context.Context, c *localCollection) error {
				return e.collectPreferences(ctx, c.userID, c.objects)
			},
			collectNeedsUser: true,
		},
		{
			kind: KindStateRule, rank: 9, optIn: optInStateRule, strictID: true, idInPayload: true,
			newPayload: newPayload[stateRuleObject],
			apply: func(e *Engine, ctx context.Context, _ string, plaintext []byte, report *SyncReport, _ string) (bool, bool) {
				return e.applyStateRuleObject(ctx, plaintext, report)
			},
			applyTombstone: func(e *Engine, ctx context.Context, objectID string, tombstone tombstoneObject, report *SyncReport, _ string) (bool, bool) {
				return e.applyStateRuleTombstone(ctx, objectID, tombstone.DeletedAt, report)
			},
			collect:        (*Engine).collectStateRuleObjects,
			browserCollect: true,
		},
		{
			kind: KindRunbookTemplate, rank: 10, optIn: optInRunbookTemplate, strictID: true, idInPayload: true,
			newPayload: newPayload[runbookTemplateObject],
			apply: func(e *Engine, ctx context.Context, _ string, plaintext []byte, report *SyncReport, _ string) (bool, bool) {
				return e.applyRunbookTemplateObject(ctx, plaintext, report)
			},
			applyTombstone: func(e *Engine, ctx context.Context, objectID string, tombstone tombstoneObject, report *SyncReport, _ string) (bool, bool) {
				return e.applyRunbookTemplateTombstone(ctx, objectID, tombstone.DeletedAt, report)
			},
			collect:        (*Engine).collectRunbookTemplateObjects,
			browserCollect: true,
		},
		{
			kind: KindSkill, rank: 11, optIn: optInSkill, aiOwner: true, idInPayload: true,
			newPayload: newPayload[skillObject],
			apply: func(e *Engine, ctx context.Context, objectID string, plaintext []byte, report *SyncReport, _ string) (bool, bool) {
				return e.applySkillObject(ctx, objectID, plaintext, report)
			},
			applyTombstone: func(e *Engine, ctx context.Context, objectID string, tombstone tombstoneObject, report *SyncReport, _ string) (bool, bool) {
				return e.applySkillTombstone(ctx, objectID, tombstone.DeletedAt, report)
			},
			collect:        (*Engine).collectSkillObjects,
			browserCollect: true,
		},
		{
			kind: KindMemory, rank: 12, optIn: optInMemory, aiOwner: true, idInPayload: true,
			newPayload:  newPayload[memoryObject],
			displayName: memoryDisplayName,
			apply: func(e *Engine, ctx context.Context, _ string, plaintext []byte, report *SyncReport, _ string) (bool, bool) {
				return e.applyMemoryObject(ctx, plaintext, report)
			},
			applyTombstone: func(e *Engine, ctx context.Context, objectID string, tombstone tombstoneObject, report *SyncReport, _ string) (bool, bool) {
				return e.applyMemoryTombstone(ctx, objectID, tombstone.DeletedAt, report)
			},
			collect:        (*Engine).collectMemoryObjects,
			browserCollect: true,
		},
		{
			kind: KindCron, rank: 13, optIn: optInCron, aiOwner: true, idInPayload: true,
			newPayload: newPayload[cronObject],
			apply: func(e *Engine, ctx context.Context, _ string, plaintext []byte, report *SyncReport, _ string) (bool, bool) {
				return e.applyCronObject(ctx, plaintext, report)
			},
			applyTombstone: func(e *Engine, ctx context.Context, objectID string, tombstone tombstoneObject, report *SyncReport, _ string) (bool, bool) {
				return e.applyCronTombstone(ctx, objectID, tombstone.DeletedAt, report)
			},
			collect:        (*Engine).collectCronObjects,
			browserCollect: true,
		},
		{
			kind: KindTombstone, rank: 14, revision: revisionDeleted,
			newPayload:  newPayload[tombstoneObject],
			displayName: func(objectID string, _ []byte) string { return objectID },
			apply: func(e *Engine, ctx context.Context, objectID string, plaintext []byte, report *SyncReport, userID string) (bool, bool) {
				return e.applyTombstoneObject(ctx, objectID, plaintext, report, userID)
			},
		},
	}
	validateKindRegistry()
}

func validateKindRegistry() {
	optInDeclared := make(map[optInID]bool, len(optInSpecs))
	for _, spec := range optInSpecs {
		if spec.id <= optInNone || spec.id >= optInCount || optInDeclared[spec.id] {
			panic("invalid or duplicate sync opt-in spec")
		}
		optInDeclared[spec.id] = true
	}
	syncKindMap = make(map[string]*kindSpec, len(syncKinds))
	ranks := make(map[int]string, len(syncKinds))
	aiOwnerKinds = nil
	unknownKindRank = 0
	for index := range syncKinds {
		spec := &syncKinds[index]
		if _, exists := syncKindMap[spec.kind]; exists {
			panic("duplicate sync kind " + spec.kind)
		}
		if other, exists := ranks[spec.rank]; exists {
			panic("sync kinds " + other + " and " + spec.kind + " share a rank")
		}
		if spec.newPayload == nil || spec.apply == nil {
			panic("incomplete sync kind " + spec.kind)
		}
		if (spec.kind == KindTombstone) != (spec.collect == nil) {
			panic("sync kind " + spec.kind + ": only tombstones are collected outside the registry loop")
		}
		if spec.optIn != optInNone && !optInDeclared[spec.optIn] {
			panic("sync kind " + spec.kind + " uses an undeclared opt-in")
		}
		syncKindMap[spec.kind] = spec
		ranks[spec.rank] = spec.kind
		unknownKindRank = max(unknownKindRank, spec.rank+1)
		if spec.aiOwner {
			aiOwnerKinds = append(aiOwnerKinds, spec.kind)
		}
	}
	if _, ok := syncKindMap[KindTombstone]; !ok {
		panic("sync kind registry lacks tombstones")
	}
	if len(decryptOrder) != len(syncKinds) {
		panic("decryptOrder must list every sync kind once")
	}
	seen := make(map[string]bool, len(decryptOrder))
	for _, kind := range decryptOrder {
		if _, ok := syncKindMap[kind]; !ok || seen[kind] {
			panic("decryptOrder must list every sync kind once")
		}
		seen[kind] = true
	}
	objectKinds = append([]string(nil), decryptOrder...)
}

func lookupKind(kind string) (*kindSpec, bool) {
	spec, ok := syncKindMap[kind]
	return spec, ok
}

func newPayload[T any](string) (any, error) {
	return new(T), nil
}

// newAIGrantPayload picks the ai_grant entity family from the object ID prefix.
func newAIGrantPayload(objectID string) (any, error) {
	switch {
	case strings.HasPrefix(objectID, aiGrantDevicePrefix):
		return &aiGrantDeviceObject{}, nil
	case strings.HasPrefix(objectID, aiGrantRulePrefix):
		return &aiGrantRuleObject{}, nil
	case strings.HasPrefix(objectID, aiGrantModePrefix):
		return &aiGrantModeObject{}, nil
	default:
		return nil, ipc.NewError(ipc.CodeBadParam, "AI 授权对象 ID 不受支持")
	}
}

// objectKindRank orders kinds by dependency; group ancestry is sorted separately within the group kind.
// Plain tombstones rank after content objects but before session records, matching the browser push order.
func objectKindRank(kind string) int {
	if spec, ok := lookupKind(kind); ok {
		return spec.rank
	}
	return unknownKindRank
}

// kindOptIn is the per-sync-round opt-in snapshot of sync object kinds, indexed by optInID; the zero value means hard-off.
type kindOptIn [optInCount]bool

func allKindOptIn() kindOptIn {
	var optIn kindOptIn
	for _, spec := range optInSpecs {
		optIn[spec.id] = true
	}
	return optIn
}

func (o kindOptIn) enabled(id optInID) bool {
	return id == optInNone || o[id]
}

// allowsKind reports whether objects of kind (or tombstones targeting it) sync this round; unknown kinds are not gated.
func (o kindOptIn) allowsKind(kind string) bool {
	spec, ok := lookupKind(kind)
	return !ok || o.enabled(spec.optIn)
}

func (o kindOptIn) view() KindOptIn {
	var view KindOptIn
	for _, spec := range optInSpecs {
		*spec.view(&view) = o[spec.id]
	}
	return view
}

// loadKindOptIn reads the sync object opt-in for userID in optInSpecs order.
func loadKindOptIn(ctx context.Context, database *store.Store, userID string) (kindOptIn, error) {
	var optIn kindOptIn
	for _, spec := range optInSpecs {
		enabled, err := spec.get(database, ctx, userID)
		if err != nil {
			return kindOptIn{}, err
		}
		optIn[spec.id] = enabled
	}
	return optIn, nil
}
