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

// kindSpec describes everything kind-specific about one sync object kind. Adding a kind means adding one spec.
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
	collect func(e *Engine, ctx context.Context, c *localCollection) error
	// collectNeedsUser skips collection without a session user.
	collectNeedsUser bool
}

// syncKinds is the kind registry in collection order. It is assigned in init because spec functions refer back to the registry.
var (
	syncKinds   []kindSpec
	syncKindMap map[string]*kindSpec
	// objectKinds is the decryption attempt order; AAD binds kind, so a wrong kind can never decrypt.
	objectKinds []string
)

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
			kind: KindTranscript, rank: 10, strictID: true, idInPayload: true, revision: revisionEnded,
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
			// Tombstones collect last: a tombstone replaces a live object only when it is newer.
			kind: KindTombstone, rank: 9, revision: revisionDeleted,
			newPayload:  newPayload[tombstoneObject],
			displayName: func(objectID string, _ []byte) string { return objectID },
			apply: func(e *Engine, ctx context.Context, objectID string, plaintext []byte, report *SyncReport, userID string) (bool, bool) {
				return e.applyTombstoneObject(ctx, objectID, plaintext, report, userID)
			},
			collect: (*Engine).collectTombstoneObjects,
		},
	}
	syncKindMap = make(map[string]*kindSpec, len(syncKinds))
	objectKinds = make([]string, 0, len(syncKinds))
	for index := range syncKinds {
		spec := &syncKinds[index]
		if _, exists := syncKindMap[spec.kind]; exists {
			panic("duplicate sync kind " + spec.kind)
		}
		if spec.newPayload == nil || spec.apply == nil || spec.collect == nil {
			panic("incomplete sync kind " + spec.kind)
		}
		syncKindMap[spec.kind] = spec
		objectKinds = append(objectKinds, spec.kind)
	}
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
	return len(syncKinds)
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
