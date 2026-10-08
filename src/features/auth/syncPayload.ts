import type { AssetGroup, TranscriptChunk, TranscriptSummary } from "../../ipc/commands";
import type { SnippetDto, SyncCollectAIProfile, SyncCollectAsset, SyncCollectCredential, SyncCollectKnownHost, SyncCollectTombstone } from "../../ipc/types";

// 本文件的载荷构造与 internal/sync/object.go 的 Go 结构体逐字段对齐(字段顺序 + omitempty 语义),
// 并经 marshalSyncPayload 产出与 Go json.Marshal 逐字节一致的文本;跨端 payload hash 相同,
// 已一致的对象才不会被反复推送/应用。字节级 fixtures 见 src/test/auth-sync-fixtures.ts(真实 Go 产出)。

// marshalSyncPayload 复刻 Go json.Marshal 的默认 HTML 转义(< > & 与 U+2028/U+2029);
// 这些字符只会出现在 JSON 字符串字面量里,全局替换不会碰到结构字符或既有转义序列。
export function marshalSyncPayload(payload: unknown): string {
  return JSON.stringify(payload)
    .replace(/&/g, "\\u0026")
    .replace(/</g, "\\u003c")
    .replace(/>/g, "\\u003e")
    .replace(/\u2028/g, "\\u2028")
    .replace(/\u2029/g, "\\u2029");
}

// groupPayload 对齐 groupObject: id, parentId(omitempty), name, sort, createdAt, updatedAt。
export function groupPayload(g: AssetGroup): unknown {
  const o: Record<string, unknown> = { id: g.id };
  if (g.parentId !== null) o.parentId = g.parentId;
  o.name = g.name;
  o.sort = g.sort;
  o.createdAt = g.createdAt;
  o.updatedAt = g.updatedAt;
  return o;
}

// collectAssetPayload 对齐 assetObject(M141 collect 输出与 Go 结构同字段同序)。
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

// snippetPayload 对齐 snippetObject: id, groupId(omitempty), name, body, sort, createdAt, updatedAt。
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

// tombstonePayload 对齐 tombstoneObject: 与目标对象同 ID, deletedAt 即修订号。
// known_host 冲突墓碑必须原样携带败者三元组(host/port/keyType, omitempty), 否则再传播的墓碑退化为
// 用户删除语义, 下游设备会把胜出的较新化身误删(M163 R4 语义); 用户主动删除墓碑不带三元组。
export function tombstonePayload(t: SyncCollectTombstone): unknown {
  const o: Record<string, unknown> = { targetKind: t.targetKind, deletedAt: t.deletedAt };
  if (t.host) o.host = t.host;
  if (t.port) o.port = t.port;
  if (t.keyType) o.keyType = t.keyType;
  return o;
}

// knownHostPayload 对齐 knownHostObject: id, host, port, keyType, fingerprint, addedAt(全部必填, 无 omitempty);
// addedAt 即 LWW 修订号(重新接受主机密钥会刷新)。
export function knownHostPayload(k: SyncCollectKnownHost): unknown {
  return { id: k.id, host: k.host, port: k.port, keyType: k.keyType, fingerprint: k.fingerprint, addedAt: k.addedAt };
}

// aiProfilePayload 对齐 aiProfileObject: 字段顺序与 Go 结构体一致; fallbackModel 空串与 maxTokens/
// 各超时空指针走 omitempty 省略, proxy/apiKey 无 omitempty(缺省输出 null/空串)。apiKey 恒为明文
// (整体经 DEK 端到端加密), 只有 collect 以 revealed 状态给出明文时才构造载荷。
export function aiProfilePayload(p: SyncCollectAIProfile): unknown {
  const o: Record<string, unknown> = { id: p.id, name: p.name, baseUrl: p.baseUrl, apiKey: p.apiKey ?? "", model: p.model };
  if (p.fallbackModel) o.fallbackModel = p.fallbackModel;
  o.temperature = p.temperature;
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

// credentialPayload 对齐 credentialObject; secret 缺失的凭据不会走到这里(收集期已 warning)。
export function credentialPayload(c: SyncCollectCredential): unknown {
  return { id: c.id, name: c.name, kind: c.kind, secret: c.secret ?? "", updatedAt: c.updatedAt };
}

// transcriptPayload 对齐 transcriptObject 的字段顺序(id, sessionId 紧随其后)与 omitempty:
// 空内容不输出 content 字段(与 Go 空切片一致),contentOmitted 只在省略时输出。
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
