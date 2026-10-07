import type { AssetGroup, TranscriptChunk, TranscriptSummary } from "../../ipc/commands";
import type { SnippetDto, SyncCollectAsset, SyncCollectCredential, SyncCollectTombstone } from "../../ipc/types";

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
export function tombstonePayload(t: SyncCollectTombstone): unknown {
  return { targetKind: t.targetKind, deletedAt: t.deletedAt };
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
    o.content = content.map((c) => ({ seq: c.seq, tabId: c.tabId, ts: c.ts, data: c.dataBase64 }));
  }
  return o;
}
