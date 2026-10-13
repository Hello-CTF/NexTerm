import type { AssetGroup, TranscriptChunk, TranscriptSummary } from "../../ipc/commands";
import type { SnippetDto, SyncCollectAIGrantDevice, SyncCollectAIGrantMode, SyncCollectAIGrantRule, SyncCollectAIPermission, SyncCollectAIProfile, SyncCollectAsset, SyncCollectCredential, SyncCollectKnownHost, SyncCollectPreference, SyncCollectTombstone } from "../../ipc/types";

// Payload construction in this file aligns field by field with the Go structs in internal/sync/object.go (field order + omitempty semantics),
// and marshalSyncPayload produces text byte-identical to Go json.Marshal; cross-side payload hashes match,
// so already-identical objects are not repeatedly pushed/applied. Byte-level fixtures live in src/test/auth-sync-fixtures.ts (real Go output).

// marshalSyncPayload replicates Go json.Marshal's default HTML escaping (< > & and U+2028/U+2029);
// these characters only appear inside JSON string literals, so a global replace never touches structural characters or existing escape sequences.
export function marshalSyncPayload(payload: unknown): string {
  return JSON.stringify(payload)
    .replace(/&/g, "\\u0026")
    .replace(/</g, "\\u003c")
    .replace(/>/g, "\\u003e")
    .replace(/\u2028/g, "\\u2028")
    .replace(/\u2029/g, "\\u2029");
}

// groupPayload mirrors groupObject: id, parentId(omitempty), name, sort, createdAt, updatedAt.
export function groupPayload(g: AssetGroup): unknown {
  const o: Record<string, unknown> = { id: g.id };
  if (g.parentId !== null) o.parentId = g.parentId;
  o.name = g.name;
  o.sort = g.sort;
  o.createdAt = g.createdAt;
  o.updatedAt = g.updatedAt;
  return o;
}

// collectAssetPayload mirrors assetObject (M141 collect output has the same fields in the same order as the Go struct).
export function collectAssetPayload(a: SyncCollectAsset): unknown {
  const o: Record<string, unknown> = { id: a.id };
  if (a.groupId !== undefined) o.groupId = a.groupId;
  o.kind = a.kind;
  o.name = a.name;
  if (a.host !== undefined) o.host = a.host;
  if (a.port !== undefined) o.port = a.port;
  if (a.username !== undefined) o.username = a.username;
  if (a.authKind !== undefined) o.authKind = a.authKind;
  if (a.keyPath !== undefined) o.keyPath = a.keyPath;
  if (a.credId !== undefined) o.credId = a.credId;
  o.optionsJson = a.optionsJson;
  o.tags = a.tags;
  o.note = a.note;
  o.sort = a.sort;
  o.createdAt = a.createdAt;
  o.updatedAt = a.updatedAt;
  if (a.deletedAt !== undefined) o.deletedAt = a.deletedAt;
  return o;
}

// snippetPayload mirrors snippetObject: id, groupId(omitempty), name, body, sort, createdAt, updatedAt.
export function snippetPayload(s: SnippetDto): unknown {
  const o: Record<string, unknown> = { id: s.id };
  if (s.groupId !== null) o.groupId = s.groupId;
  o.name = s.name;
  o.body = s.body;
  o.sort = s.sort;
  o.createdAt = s.createdAt;
  o.updatedAt = s.updatedAt;
  return o;
}

// tombstonePayload mirrors tombstoneObject: same ID as the target object, deletedAt is the revision number.
// A known_host conflict tombstone must carry the loser triple (host/port/keyType, omitempty) as-is; otherwise the re-propagated tombstone degrades into
// user-delete semantics and downstream devices would wrongly delete the winning newer incarnation (M163 R4 semantics); user-initiated delete tombstones carry no triple.
export function tombstonePayload(t: SyncCollectTombstone): unknown {
  const o: Record<string, unknown> = { targetKind: t.targetKind, deletedAt: t.deletedAt };
  if (t.host) o.host = t.host;
  if (t.port) o.port = t.port;
  if (t.keyType) o.keyType = t.keyType;
  return o;
}

// knownHostPayload mirrors knownHostObject: id, host, port, keyType, fingerprint, addedAt (all required, no omitempty);
// addedAt is the LWW revision number (re-accepting a host key refreshes it).
export function knownHostPayload(k: SyncCollectKnownHost): unknown {
  return { id: k.id, host: k.host, port: k.port, keyType: k.keyType, fingerprint: k.fingerprint, addedAt: k.addedAt };
}

// aiProfilePayload mirrors aiProfileObject: field order matches the Go struct; empty fallbackModel and maxTokens/
// timeout pointers are omitted via omitempty, proxy/apiKey have no omitempty (default output null/empty string). apiKey is always plaintext
// (the whole thing is end-to-end encrypted with the DEK), and the payload is only built when collect yields plaintext in revealed state.
export function aiProfilePayload(p: SyncCollectAIProfile): unknown {
  const o: Record<string, unknown> = { id: p.id, name: p.name, baseUrl: p.baseUrl, apiKey: p.apiKey ?? "", model: p.model };
  if (p.fallbackModel) o.fallbackModel = p.fallbackModel;
  o.temperature = p.temperature;
  if (p.reasoningEffort) o.reasoningEffort = p.reasoningEffort;
  o.contextWindow = p.contextWindow;
  if (p.maxTokens !== undefined && p.maxTokens !== null) o.maxTokens = p.maxTokens;
  o.proxy = p.proxy ?? null;
  o.stream = p.stream;
  if (p.requestTimeoutSeconds !== undefined && p.requestTimeoutSeconds !== null) o.requestTimeoutSeconds = p.requestTimeoutSeconds;
  if (p.idleTimeoutSeconds !== undefined && p.idleTimeoutSeconds !== null) o.idleTimeoutSeconds = p.idleTimeoutSeconds;
  if (p.circuitFailureThreshold !== undefined && p.circuitFailureThreshold !== null) o.circuitFailureThreshold = p.circuitFailureThreshold;
  if (p.circuitCooldownSeconds !== undefined && p.circuitCooldownSeconds !== null) o.circuitCooldownSeconds = p.circuitCooldownSeconds;
  o.updatedAt = p.updatedAt;
  return o;
}

export function aiPermissionPayload(p: SyncCollectAIPermission): unknown {
  return { id: p.id, mode: p.mode, dangerRules: p.dangerRules, updatedAt: p.updatedAt };
}

// aiGrant*Payload mirror aiGrantDeviceObject/aiGrantRuleObject/aiGrantModeObject: field order matches the Go structs;
// rule path/expiresAt are omitted via omitempty when empty/zero, kinds has no omitempty (always an array).
export function aiGrantDevicePayload(g: SyncCollectAIGrantDevice): unknown {
  return { id: g.id, deviceId: g.deviceId, kinds: g.kinds, updatedAt: g.updatedAt };
}

export function aiGrantRulePayload(r: SyncCollectAIGrantRule): unknown {
  const o: Record<string, unknown> = { id: r.id, deviceId: r.deviceId, action: r.action };
  if (r.path) o.path = r.path;
  if (r.expiresAt) o.expiresAt = r.expiresAt;
  o.createdAt = r.createdAt;
  o.updatedAt = r.updatedAt;
  return o;
}

export function aiGrantModePayload(m: SyncCollectAIGrantMode): unknown {
  return { id: m.id, deviceId: m.deviceId, mode: m.mode, updatedAt: m.updatedAt };
}

export function preferencePayload(p: SyncCollectPreference): unknown {
  return { id: p.id, key: p.key, value: p.value, updatedAt: p.updatedAt };
}

// credentialPayload mirrors credentialObject; credentials missing a secret never reach this point (already warned during collect).
export function credentialPayload(c: SyncCollectCredential): unknown {
  return { id: c.id, name: c.name, kind: c.kind, secret: c.secret ?? "", updatedAt: c.updatedAt };
}

// transcriptPayload mirrors transcriptObject's field order (id, sessionId right after) and omitempty:
// empty content omits the content field (matching a Go empty slice); contentOmitted is output only when omitted.
export function transcriptPayload(t: TranscriptSummary, content: TranscriptChunk[] | null): unknown {
  const o: Record<string, unknown> = { id: t.id };
  if (t.sessionId) o.sessionId = t.sessionId;
  o.assetId = t.assetId;
  o.assetName = t.assetName;
  o.assetKind = t.assetKind;
  o.startedAt = t.startedAt;
  o.endedAt = t.endedAt ?? 0;
  o.bytes = t.bytes;
  o.chunks = t.chunks;
  o.truncated = t.truncated;
  if (t.contentOmitted || content === null) {
    o.contentOmitted = true;
    return o;
  }
  if (content.length > 0) {
    o.content = content.map((c) => {
      const entry: Record<string, unknown> = { seq: c.seq, tabId: c.tabId, ts: c.ts };
      if (c.kind) entry.kind = c.kind;
      entry.data = c.dataBase64;
      return entry;
    });
  }
  return o;
}
